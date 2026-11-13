package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	if got := stripHeader(out); got != normalizedDeployment {
		t.Fatalf("got:\n%s\nwant:\n%s", got, normalizedDeployment)
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

	// Out dir exists now: fails without -r, works with -r.
	code, _, errOut = runCLI(t, "", "norm", filepath.Dir(in), "-o", out)
	if code != 2 || !strings.Contains(errOut, "already exists") {
		t.Fatalf("expected exit 2 'already exists', got %d: %s", code, errOut)
	}

	code, _, errOut = runCLI(t, "", "norm", filepath.Dir(in), "-o", out, "-r")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
}

func TestNorm_Check(t *testing.T) {
	t.Parallel()

	code, out, errOut := runCLI(t, deploymentYAML, "norm", "-", "--check")
	if code != 1 {
		t.Fatalf("expected exit 1, got %d: %s", code, errOut)
	}

	if !strings.Contains(out, "-  uid:") || !strings.Contains(errOut, "is not normalized") {
		t.Fatalf("unexpected output:\nstdout:\n%s\nstderr:\n%s", out, errOut)
	}

	code, out, errOut = runCLI(t, normalizedDeployment, "norm", "-", "--check")
	if code != 0 || out != "" {
		t.Fatalf("expected exit 0 and no output, got %d: %s %s", code, out, errOut)
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

func TestShowCommonConfig(t *testing.T) {
	t.Parallel()

	code, out, _ := runCLI(t, "", "show-common-config")
	if code != 0 || !strings.Contains(out, "removeFields:") {
		t.Fatalf("got %d:\n%s", code, out)
	}
}

func TestNorm_SOPS(t *testing.T) {
	t.Parallel()

	in := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n  namespace: n\ndata:\n  pw: ENC[x]\nsops:\n  mac: ENC[y]\n"

	code, _, errOut := runCLI(t, in, "norm", "-")
	if code != 2 || !strings.Contains(errOut, "SOPS") {
		t.Fatalf("expected exit 2 with SOPS error, got %d: %s", code, errOut)
	}

	code, out, errOut := runCLI(t, in, "norm", "-", "--skip-sops")
	if code != 0 || strings.Contains(out, "kind: Secret") {
		t.Fatalf("expected the secret to be skipped, got %d:\n%s%s", code, out, errOut)
	}

	code, _, errOut = runCLI(t, in, "norm", "-", "--check")
	if code != 2 || !strings.Contains(errOut, "SOPS") {
		t.Fatalf("expected --check to fail with SOPS error, got %d: %s", code, errOut)
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
