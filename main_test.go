package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

const deploymentYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: demo
  uid: 1234
  resourceVersion: "42"
  creationTimestamp: "2024-01-01T00:00:00Z"
spec:
  progressDeadlineSeconds: 600
  replicas: 2
  template:
    spec:
      containers:
        - name: web
          image: nginx:1.27
          imagePullPolicy: IfNotPresent
status:
  readyReplicas: 2
`

const normalizedDeployment = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: demo
spec:
  replicas: 2
  template:
    spec:
      containers:
      - image: nginx:1.27
        name: web
`

func runCLI(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := run(args, strings.NewReader(stdin), &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatal(err)
	}
}

func stripHeader(s string) string {
	_, rest, _ := strings.Cut(s, "\n")
	return rest
}

func TestNorm_StdinToStdout(t *testing.T) {
	t.Parallel()

	code, out, errOut := runCLI(t, deploymentYAML, "norm", "-")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	if !strings.HasPrefix(out, "# https://github.com/guettli/kubenorm version: ") {
		t.Fatalf("missing header:\n%s", out)
	}

	if got := stripHeader(out); got != "---\n"+normalizedDeployment {
		t.Fatalf("got:\n%s\nwant:\n---\n%s", got, normalizedDeployment)
	}
}

func TestNorm_OutDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	in := filepath.Join(dir, "in", "deploy.yaml")
	out := filepath.Join(dir, "out")

	writeFile(t, in, deploymentYAML)

	code, _, errOut := runCLI(t, "", "norm", filepath.Dir(in), "-o", out, "--comment", "generated")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	data, err := os.ReadFile(filepath.Join(out, "demo", "apps_Deployment", "web.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.SplitN(string(data), "\n", 3)
	if lines[1] != "# generated" || lines[2] != normalizedDeployment {
		t.Fatalf("unexpected content:\n%s", data)
	}

	// Out dir exists now: fails without --clean-out-dir, works with it.
	code, _, errOut = runCLI(t, "", "norm", filepath.Dir(in), "-o", out)
	if code != 2 || !strings.Contains(errOut, "already exists") {
		t.Fatalf("expected exit 2 'already exists', got %d: %s", code, errOut)
	}

	code, _, errOut = runCLI(t, "", "norm", filepath.Dir(in), "--clean-out-dir", "-o", out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	// Deprecated name: still works, warns on stderr.
	code, _, errOut = runCLI(t, "", "norm", filepath.Dir(in), "-r", "-o", out)
	if code != 0 || !strings.Contains(errOut, "use --clean-out-dir") {
		t.Fatalf("expected exit 0 with deprecation warning, got %d: %s", code, errOut)
	}
}

func TestNorm_RemoveOutDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	in := filepath.Join(dir, "in", "app.yaml")
	out := filepath.Join(dir, "out")

	writeFile(t, in, deploymentYAML)
	// A resource that was deleted from the input since the last run.
	stale := filepath.Join(out, "demo", "ConfigMap", "old.yaml")
	writeFile(t, stale, "kind: ConfigMap\n")

	code, _, errOut := runCLI(t, "", "norm", filepath.Dir(in), "--clean-out-dir", "-o", out)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	_, err := os.Stat(stale)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale file was not removed: %v", err)
	}

	// Anything but *.yaml files: refuse and keep everything.
	other := filepath.Join(out, "notes.txt")
	writeFile(t, other, "keep me\n")

	code, _, errOut = runCLI(t, "", "norm", filepath.Dir(in), "--clean-out-dir", "-o", out)
	if code != 2 || !strings.Contains(errOut, "refusing to remove") {
		t.Fatalf("expected exit 2 'refusing to remove', got %d: %s", code, errOut)
	}

	_, err = os.Stat(other)
	if err != nil {
		t.Fatalf("file was removed: %v", err)
	}
}

func TestNorm_Check(t *testing.T) {
	t.Parallel()

	code, out, errOut := runCLI(t, deploymentYAML, "norm", "-", "--check")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d: %s", code, errOut)
	}

	if !strings.Contains(out, "-  uid:") || !strings.Contains(errOut, "not normalized") {
		t.Fatalf("unexpected output:\nstdout:\n%s\nstderr:\n%s", out, errOut)
	}

	code, out, errOut = runCLI(t, normalizedDeployment, "norm", "-", "--check")
	if code != 0 || out != "" {
		t.Fatalf("expected exit 0 and no output, got %d: %s %s", code, out, errOut)
	}
}

func TestNorm_CheckAddNamespaceIfMissing(t *testing.T) {
	t.Parallel()

	in := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\ndata:\n  k: v\n"

	code, out, errOut := runCLI(t, in, "norm", "-", "--check", "-n", "demo", "--add-namespace-if-missing", "demo")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d: %s", code, errOut)
	}

	if strings.Contains(out, devNull) || !strings.Contains(out, "+  namespace: demo") || strings.Count(out, "\n+") != 2 {
		t.Fatalf("expected a one-line diff:\n%s", out)
	}
}

func TestNorm_CheckSetLabel(t *testing.T) {
	t.Parallel()

	in := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n  namespace: demo\n"

	code, out, errOut := runCLI(t, in, "norm", "-", "--check", "--set-label", "team=x")
	if code != 1 || !strings.Contains(out, "+    team: x") {
		t.Fatalf("expected exit 1 with the label as diff, got %d:\n%s%s", code, out, errOut)
	}

	code, normalized, errOut := runCLI(t, in, "norm", "-", "--set-label", "team=x")
	if code != 0 {
		t.Fatalf("got %d: %s", code, errOut)
	}

	for _, args := range [][]string{{"--set-label", "team=x"}, nil} {
		code, out, errOut = runCLI(t, normalized, append([]string{"norm", "-", "--check"}, args...)...)
		if code != 0 {
			t.Fatalf("%v: expected exit 0, got %d:\n%s%s", args, code, out, errOut)
		}
	}
}

func TestNorm_CheckFailsOnDroppedResource(t *testing.T) {
	t.Parallel()

	pod := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: p\n  namespace: demo\n"

	code, out, errOut := runCLI(t, pod, "norm", "-", "--check")
	if code != 1 || !strings.Contains(out, "-kind: Pod") {
		t.Fatalf("expected exit 1 with the Pod as deleted, got %d: %s %s", code, out, errOut)
	}

	// Outside the scope filters: not checked.
	code, _, errOut = runCLI(t, pod, "norm", "-", "--check", "--kind", "ConfigMap")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, errOut)
	}
}

func TestNorm_CheckAndOutDirExclusive(t *testing.T) {
	t.Parallel()

	code, _, errOut := runCLI(t, "", "norm", "-", "--check", "-o", t.TempDir())
	if code != 2 || !strings.Contains(errOut, "mutually exclusive") {
		t.Fatalf("got %d: %s", code, errOut)
	}
}

func TestNorm_SecretsNotRedactedByDefault(t *testing.T) {
	t.Parallel()

	//nolint:gosec // test fixture, not a credential
	secret := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n  namespace: n\ndata:\n  pw: c2VjcmV0\n"

	_, out, _ := runCLI(t, secret, "norm", "-")
	if !strings.Contains(out, "pw: c2VjcmV0") {
		t.Fatalf("expected secret value to be kept:\n%s", out)
	}

	_, out, _ = runCLI(t, secret, "norm", "-", "--redact-secrets")
	if strings.Contains(out, "c2VjcmV0") {
		t.Fatalf("expected secret value to be redacted:\n%s", out)
	}

	_, out, _ = runCLI(t, secret, "norm", "-", "--secrets-as-stringdata")
	if !strings.Contains(out, "pw: secret") {
		t.Fatalf("expected stringData:\n%s", out)
	}
}

func TestNorm_AddNamespaceIfMissing(t *testing.T) {
	t.Parallel()

	in := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n---\napiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: r\n"

	code, out, errOut := runCLI(t, in, "norm", "-", "--add-namespace-if-missing", "liqo")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	if strings.Count(out, "namespace: liqo") != 1 {
		t.Fatalf("expected only the ConfigMap to get a namespace:\n%s", out)
	}

	code, _, errOut = runCLI(t, in, "norm", "-", "--add-namespace-if-missing", "a", "--overwrite-namespace", "b")
	if code != 2 || !strings.Contains(errOut, "mutually exclusive") {
		t.Fatalf("got %d: %s", code, errOut)
	}
}

func TestDiff(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	b := filepath.Join(dir, "b")

	writeFile(t, a, deploymentYAML)
	// Same resource, other file name, other key order, server fields: equal.
	writeFile(t, filepath.Join(b, "x", "whatever.yml"), normalizedDeployment)

	code, out, errOut := runCLI(t, "", "diff", a, b)
	if code != 0 || out != "" {
		t.Fatalf("expected no diff, got %d:\n%s%s", code, out, errOut)
	}

	changed := strings.Replace(deploymentYAML, "replicas: 2", "replicas: 3", 1)

	code, out, _ = runCLI(t, changed, "diff", b, "-")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}

	for _, want := range []string{"+++ stdin/demo/apps_Deployment/web.yaml", "-  replicas: 2", "+  replicas: 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	code, _, errOut = runCLI(t, "", "diff", "-", "-")
	if code != 2 || !strings.Contains(errOut, "stdin") {
		t.Fatalf("got %d: %s", code, errOut)
	}
}

func TestDiff_OneSideOnly(t *testing.T) {
	t.Parallel()

	b := t.TempDir()
	writeFile(t, filepath.Join(b, "web.yaml"), normalizedDeployment)

	changed := strings.Replace(deploymentYAML, "replicas: 2", "replicas: 3", 1)

	extra := changed + "---\n" + strings.Replace(normalizedDeployment, "name: web", "name: api", 1)

	code, out, _ := runCLI(t, extra, "diff", b, "-")
	if code != 1 || !strings.Contains(out, "Only here (new): stdin (demo/apps_Deployment/api.yaml)\n") || strings.Contains(out, "/dev/null") {
		t.Fatalf("expected an Only here line, got %d:\n%s", code, out)
	}

	code, out, _ = runCLI(t, extra, "diff", "--full", b, "-")
	if code != 1 || !strings.Contains(out, "+++ b/demo/apps_Deployment/api.yaml") || !strings.Contains(out, "--- /dev/null") {
		t.Fatalf("expected the full content with --full, got %d:\n%s", code, out)
	}

	code, out, _ = runCLI(t, changed, "diff", "-", b)
	if code != 1 || !strings.Contains(out, "+  replicas: 2") {
		t.Fatalf("expected a diff, got %d:\n%s", code, out)
	}

	code, out, _ = runCLI(t, extra, "diff", "-", b)
	if code != 1 || !strings.Contains(out, "Only here (old): stdin (demo/apps_Deployment/api.yaml)\n") {
		t.Fatalf("expected an Only here line for the old side, got %d:\n%s", code, out)
	}

	// A file in a directory is named by its real path, not by the input
	// argument plus the normalized path.
	dumped := filepath.Join(b, "demo", "cm.yaml")
	writeFile(t, dumped, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm\n  namespace: demo\n  managedFields:\n    - manager: kubectl-edit\n      operation: Update\n")

	code, out, _ = runCLI(t, changed, "diff", filepath.Join(b, "demo"), "-")
	if want := "Only here (old): " + dumped + " managed by: manager kubectl-edit\n"; code != 1 || !strings.Contains(out, want) {
		t.Fatalf("expected %q, got %d:\n%s", want, code, out)
	}
}

func TestShowCommonConfig(t *testing.T) {
	t.Parallel()

	code, out, _ := runCLI(t, "", "show-common-config")
	if code != 0 || !strings.Contains(out, "removeFields:") {
		t.Fatalf("got %d:\n%s", code, out)
	}
}

func TestNorm_SOPS(t *testing.T) {
	t.Parallel()

	in := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n  namespace: ns\ndata:\n  pw: ENC[x]\nsops:\n  mac: ENC[y]\n---\n" + normalizedDeployment

	code, out, errOut := runCLI(t, in, "norm", "-")
	if code != 0 || strings.Contains(out, "kind: Secret") || !strings.Contains(out, "kind: Deployment") {
		t.Fatalf("expected the secret to be skipped, got %d:\n%s%s", code, out, errOut)
	}

	if !strings.Contains(errOut, "Skipped SOPS-encrypted ns/Secret/s.yaml") {
		t.Fatalf("expected a note on stderr, got: %s", errOut)
	}
}

func TestNorm_SeveralInputs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	a := filepath.Join(dir, "a.yaml")
	b := filepath.Join(dir, "sub", "b.yaml")

	writeFile(t, a, deploymentYAML)
	writeFile(t, b, strings.Replace(normalizedDeployment, "name: web", "name: api", 1))

	code, out, errOut := runCLI(t, "", "norm", a, filepath.Dir(b))
	if code != 0 || !strings.Contains(out, "name: web") || !strings.Contains(out, "name: api") {
		t.Fatalf("got %d:\n%s%s", code, out, errOut)
	}

	code, _, errOut = runCLI(t, "", "norm", a, "-")
	if code != 2 || !strings.Contains(errOut, "stdin") {
		t.Fatalf("expected stdin combination error, got %d: %s", code, errOut)
	}

	code, _, _ = runCLI(t, "", "norm", "--check", a, b)
	if code != 1 {
		t.Fatalf("expected --check to fail for a.yaml, got %d", code)
	}
}

func TestNorm_CheckSameResourceInTwoTrees(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "cluster-a", "web.yaml"), normalizedDeployment)
	writeFile(t, filepath.Join(dir, "cluster-b", "web.yaml"), strings.Replace(normalizedDeployment, "replicas: 2", "replicas: 5", 1))

	code, out, errOut := runCLI(t, "", "norm", "--check", dir)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d:\n%s%s", code, out, errOut)
	}
}

func TestExplain(t *testing.T) {
	t.Parallel()

	in := deploymentYAML + "---\napiVersion: v1\nkind: Pod\nmetadata:\n  name: p\n  namespace: demo\n"

	code, out, errOut := runCLI(t, in, "explain", "-", "--skip-name-glob", "nothing")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}

	for _, want := range []string{
		"demo/apps_Deployment/web.yaml (from -)\n",
		"  - metadata.uid: 1234  [removeFields[1] in common config: metadata.uid]\n",
		"  - status: {\"readyReplicas\":2}  [removeFields[1] in common config: status]\n",
		"  - spec.progressDeadlineSeconds: 600  [removeFields[",
		"demo/Pod/p.yaml (from -)\n  dropped: skipResources[",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestNorm_OverlappingInputs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "web.yaml")
	writeFile(t, file, normalizedDeployment)

	code, out, errOut := runCLI(t, "", "norm", dir, file)
	if code != 0 || strings.Count(out, "kind: Deployment") != 1 {
		t.Fatalf("expected the file once, got %d:\n%s%s", code, out, errOut)
	}
}

func TestFormatValue_KeepsRunes(t *testing.T) {
	t.Parallel()

	got := formatValue(strings.Repeat("ä", 40), 10)
	if !utf8.ValidString(got) || got != `"`+strings.Repeat("ä", 9)+"..." {
		t.Fatalf("formatValue = %q", got)
	}
}

func TestNorm_CRDs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "crds", "widget.yaml"), `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: widgets.example.com}
spec:
  group: example.com
  scope: Namespaced
  names: {kind: Widget}
  versions:
  - name: v1
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            properties:
              size: {anyOf: [{type: integer}, {type: string}], x-kubernetes-int-or-string: true, pattern: "^(\\+|-)?(([0-9]+(\\.[0-9]*)?)|(\\.[0-9]+))(([KMGTPE]i)|[numkMGTPE]|([eE](\\+|-)?(([0-9]+(\\.[0-9]*)?)|(\\.[0-9]+))))?$"}
`)

	in := "apiVersion: example.com/v1\nkind: Widget\nmetadata: {name: w, namespace: n}\nspec: {size: 2048Mi}\n"

	code, out, errOut := runCLI(t, in, "norm", "-", "--no-config-discovery", "--crds", filepath.Join(dir, "crds"))
	if code != 0 || !strings.Contains(out, "size: 2Gi") || strings.Contains(out, "CustomResourceDefinition") {
		t.Fatalf("exit %d: %s\n%s", code, errOut, out)
	}

	code, _, errOut = runCLI(t, in, "norm", "-", "--crds", "-")
	if code != 2 || !strings.Contains(errOut, "--crds") {
		t.Fatalf("got %d: %s", code, errOut)
	}
}

func TestNorm_SetLabelsAndAnnotations(t *testing.T) {
	t.Parallel()

	cm := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm\n  namespace: demo\n  labels:\n    team: old\n    argocd.argoproj.io/instance: shop\n"

	code, out, errOut := runCLI(t, cm, "norm", "-",
		"--set-label", "team=platform", "--set-label", "argocd.argoproj.io/instance=kept",
		"--set-annotation", "example.com/note=a=b, c")
	if code != 0 {
		t.Fatalf("got %d: %s", code, errOut)
	}

	want := `apiVersion: v1
kind: ConfigMap
metadata:
  annotations:
    example.com/note: a=b, c
  labels:
    argocd.argoproj.io/instance: kept
    team: platform
  name: cm
  namespace: demo
`
	if !strings.HasSuffix(out, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}

	code, out, _ = runCLI(t, cm, "explain", "-", "--set-label", "team=platform")
	if code != 0 || !strings.Contains(out, "--set-label team=platform") {
		t.Fatalf("explain misses the set label, got %d:\n%s", code, out)
	}

	for _, args := range [][]string{
		{"--set-label", "team"},
		{"--set-label", "=x"},
		{"--set-label", "team=a b"},
		{"--set-label", "bad key=x"},
		{"--set-annotation", "bad key=x"},
	} {
		code, _, errOut = runCLI(t, cm, append([]string{"norm", "-"}, args...)...)
		if code != 2 || errOut == "" {
			t.Errorf("%v: expected exit 2 with an error, got %d: %s", args, code, errOut)
		}
	}
}

func TestNorm_RenderedBy(t *testing.T) {
	t.Parallel()

	cm := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n  namespace: demo\n"

	code, out, errOut := runCLI(t, cm, "norm", "-", "--rendered-by", "foo/render.sh")
	if code != 0 || !strings.Contains(out, "  annotations:\n    kubenorm.guettli.github.io/rendered-by: foo/render.sh\n") {
		t.Fatalf("got %d:\n%s%s", code, out, errOut)
	}

	rendered := out

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--rendered-by", "a", "--set-annotation", "kubenorm.guettli.github.io/rendered-by=b"}, "mutually exclusive"},
		{[]string{"--rendered-by", " "}, "empty value"},
		{[]string{"--rendered-by", "a\nb"}, "control characters"},
	} {
		code, _, errOut = runCLI(t, cm, append([]string{"norm", "-"}, tc.args...)...)
		if code != 2 || !strings.Contains(errOut, tc.want) {
			t.Errorf("%q: expected exit 2 with %q, got %d: %s", tc.args, tc.want, code, errOut)
		}
	}

	code, out, _ = runCLI(t, cm, "norm", "-", "--rendered-by", "  foo/render.sh ")
	if code != 0 || out != rendered {
		t.Errorf("value not trimmed, got %d:\n%s", code, out)
	}

	code, out, _ = runCLI(t, cm, "explain", "-", "--rendered-by", "foo/render.sh")
	if code != 0 || !strings.Contains(out, "[--rendered-by kubenorm.guettli.github.io/rendered-by=foo/render.sh]") {
		t.Errorf("explain misses the --rendered-by reason, got %d:\n%s", code, out)
	}

	code, _, _ = runCLI(t, rendered, "norm", "-", "--check", "--rendered-by", "other.sh")
	if code != 1 {
		t.Errorf("expected --check to fail on another value, got %d", code)
	}

	out = rendered

	code, out, _ = runCLI(t, out, "diff", t.TempDir(), "-")
	if code != 1 || !strings.Contains(out, "managed by: rendered by foo/render.sh") {
		t.Fatalf("diff misses rendered by, got %d:\n%s", code, out)
	}
}

func TestDiff_SetLabelOnBothSides(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	old := filepath.Join(dir, "old.yaml")
	writeFile(t, old, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n  namespace: demo\n  labels:\n    team: old\n")

	code, out, errOut := runCLI(t, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n  namespace: demo\n", "diff", old, "-", "--set-label", "team=x")
	if code != 0 || out != "" {
		t.Fatalf("expected no diff, got %d:\n%s%s", code, out, errOut)
	}
}

func TestDiff_HeadersNameRealFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cm := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n  namespace: ns1\ndata:\n  k: \"1\"\n"
	oldFile := filepath.Join(dir, "a", "ns1", "x.yaml")
	newFile := filepath.Join(dir, "b", "ns1", "renamed.yaml")

	writeFile(t, oldFile, cm)
	writeFile(t, newFile, strings.Replace(cm, `"1"`, `"2"`, 1))

	// The arguments are namespace directories: before, the header was
	// a/ns1/ns1/ConfigMap/x.yaml.
	code, out, errOut := runCLI(t, "", "diff", filepath.Join(dir, "a", "ns1"), filepath.Join(dir, "b", "ns1"))
	if code != 1 || !strings.HasPrefix(out, "--- "+filepath.ToSlash(oldFile)+"\n+++ "+filepath.ToSlash(newFile)+"\n") {
		t.Fatalf("got %d:\n%s%s", code, out, errOut)
	}

	// stdin: the normalized path below it.
	code, out, errOut = runCLI(t, cm+"---\n"+strings.Replace(cm, "name: x", "name: other", 1), "diff", "-", filepath.Join(dir, "b", "ns1"))
	if code != 1 || !strings.Contains(out, "\n--- stdin/ns1/ConfigMap/x.yaml\n+++ "+filepath.ToSlash(newFile)+"\n") {
		t.Fatalf("got %d: stdout=%q stderr=%q", code, out, errOut)
	}
}

func TestDiff_HeadersSeveralResourcesAndFull(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cm := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n  namespace: ns1\ndata:\n  k: \"1\"\n"
	other := strings.Replace(cm, "name: x", "name: other", 1)

	writeFile(t, filepath.Join(dir, "old", "all.yaml"), cm+"---\n"+other)
	writeFile(t, filepath.Join(dir, "new", "all.yaml"), strings.Replace(cm, `"1"`, `"2"`, 1))

	oldDir, newDir := filepath.Join(dir, "old"), filepath.Join(dir, "new")
	slash := filepath.ToSlash

	code, out, errOut := runCLI(t, "", "diff", oldDir, newDir)
	if want := "--- " + slash(oldDir) + "/all.yaml/ns1/ConfigMap/x.yaml\n+++ " + slash(newDir) + "/all.yaml\n"; code != 1 || !strings.Contains(out, want) {
		t.Fatalf("want %q, got %d:\n%s%s", want, code, out, errOut)
	}

	// --full: a/ and b/ plus the normalized path, /dev/null for the missing
	// side.
	code, out, _ = runCLI(t, "", "diff", "--full", oldDir, newDir)
	for _, want := range []string{
		"--- a/ns1/ConfigMap/x.yaml\n+++ b/ns1/ConfigMap/x.yaml\n",
		"--- a/ns1/ConfigMap/other.yaml\n+++ /dev/null\n",
	} {
		if code != 1 || !strings.Contains(out, want) {
			t.Errorf("want %q, got %d:\n%s", want, code, out)
		}
	}
}

func TestDiff_OutputJSONNoDifferences(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.yaml"), deploymentYAML)

	code, out, errOut := runCLI(t, "", "diff", "--output", "json", filepath.Join(dir, "a.yaml"), filepath.Join(dir, "a.yaml"))
	if code != 0 || out != "{\n  \"version\": 1,\n  \"resources\": []\n}\n" {
		t.Fatalf("got %d:\n%s%s", code, out, errOut)
	}

	code, _, errOut = runCLI(t, "", "diff", "--output", "yaml", dir, dir)
	if code != 2 || !strings.Contains(errOut, "want text, json or stat") {
		t.Fatalf("got %d: %s", code, errOut)
	}

	code, _, errOut = runCLI(t, "", "diff", "--output", "stat", "--full", dir, dir)
	if code != 2 || !strings.Contains(errOut, "exclude each other") {
		t.Fatalf("got %d: %s", code, errOut)
	}
}

func TestDiff_OwnerHintsFromConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	config := filepath.Join(dir, "kubenorm.yaml")
	writeFile(t, config, "ownerHints:\n  - label: example.com/team\n    text: team {value}\n")

	cm := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n  namespace: demo\n  labels:\n    example.com/team: web\n"

	code, out, errOut := runCLI(t, cm, "diff", "--no-config-discovery", "-c", config, t.TempDir(), "-")
	if want := "Only here (new): stdin (demo/ConfigMap/c.yaml) managed by: team web\n"; code != 1 || out != want {
		t.Fatalf("want %q, got %d:\n%s%s", want, code, out, errOut)
	}

	// An annotation hint on the old side.
	writeFile(t, config, "ownerHints:\n  - annotation: example.com/tool\n")

	old := filepath.Join(dir, "old.yaml")
	writeFile(t, old, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\n  namespace: demo\n  annotations:\n    example.com/tool: pulumi\n")

	code, out, errOut = runCLI(t, "", "diff", "--no-config-discovery", "-c", config, old, t.TempDir())
	if want := "Only here (old): " + filepath.ToSlash(old) + " managed by: example.com/tool pulumi\n"; code != 1 || out != want {
		t.Fatalf("want %q, got %d:\n%s%s", want, code, out, errOut)
	}
}

// TestDiff_FullAppliesToOutDir checks that the patch of diff --full turns a
// tree of norm -o into the other one, header comments included. Not
// parallel: t.Chdir.
func TestDiff_FullAppliesToOutDir(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}

	t.Chdir(t.TempDir())

	cm := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\n  namespace: ns\ndata:\n  k: %q\n"
	oldInput := fmt.Sprintf(cm, "changed", "1") + "---\n" + fmt.Sprintf(cm, "gone", "1") + "---\n" + fmt.Sprintf(cm, "same", "1")
	newInput := fmt.Sprintf(cm, "changed", "2") + "---\n" + fmt.Sprintf(cm, "added", "1") + "---\n" + fmt.Sprintf(cm, "same", "1")

	if code, _, errOut := runCLI(t, oldInput, "norm", "-", "-o", "main/old"); code != 0 {
		t.Fatal(errOut)
	}

	if code, _, errOut := runCLI(t, newInput, "norm", "-", "-o", "new", "--comment", "rendered by render.sh"); code != 0 {
		t.Fatal(errOut)
	}

	code, patch, errOut := runCLI(t, "", "diff", "--full", "main/old", "new")
	if code != 1 {
		t.Fatalf("want differences, got %d: %s", code, errOut)
	}

	writeFile(t, "p.diff", patch)

	// A repository of its own: git apply resolves --directory against the
	// top of the repository.
	for _, args := range [][]string{{"init", "-q"}, {"apply", "--directory=main/old", "p.diff"}} {
		out, err := exec.CommandContext(t.Context(), git, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s\n%s", args, err, out, patch)
		}
	}

	read := func(dir, rel string) string {
		data, _ := os.ReadFile(filepath.Join(dir, rel))
		return string(data)
	}

	for _, rel := range []string{"ns/ConfigMap/changed.yaml", "ns/ConfigMap/added.yaml"} {
		if got, want := read("main/old", rel), read("new", rel); got != want {
			t.Errorf("%s: got:\n%s\nwant:\n%s", rel, got, want)
		}
	}

	// The unchanged resource keeps the header of the old tree.
	if got := read("main/old", "ns/ConfigMap/same.yaml"); strings.Contains(got, "render.sh") || !strings.HasPrefix(got, "# ") {
		t.Errorf("same.yaml: %s", got)
	}

	_, err = os.Stat(filepath.Join("main/old", "ns/ConfigMap/gone.yaml"))
	if !os.IsNotExist(err) {
		t.Errorf("gone.yaml not deleted: %v", err)
	}
}
