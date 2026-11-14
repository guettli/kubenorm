package normalize

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const cmA = `apiVersion: v1
kind: ConfigMap
metadata:
  name: a
  namespace: ns
data:
  k: v1
`

func renderYAML(t *testing.T, yamlDocs string) Rendered {
	t.Helper()

	resources, err := ParseYAML([]byte(yamlDocs), "test")
	if err != nil {
		t.Fatal(err)
	}

	rendered, err := Render(resources)
	if err != nil {
		t.Fatal(err)
	}

	return rendered
}

func TestRender_DuplicateIsError(t *testing.T) {
	t.Parallel()

	resources, err := ParseYAML([]byte(cmA+"---\n"+cmA), "dup.yaml")
	if err != nil {
		t.Fatal(err)
	}

	_, err = Render(resources)
	if !errors.Is(err, errDuplicate) {
		t.Fatalf("expected errDuplicate, got %v", err)
	}
}

func TestWriteStream_SortedAndSeparated(t *testing.T) {
	t.Parallel()

	rendered := renderYAML(t, strings.Replace(cmA, "name: a", "name: z", 1)+"---\n"+cmA)

	var buf bytes.Buffer

	err := rendered.WriteStream(&buf)
	if err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if strings.Count(out, "---\n") != 1 {
		t.Fatalf("expected one separator:\n%s", out)
	}

	if strings.Index(out, "name: a") > strings.Index(out, "name: z") {
		t.Fatalf("expected sorted output:\n%s", out)
	}
}

func TestParseYAML_SkipsDocumentsWithoutMetadata(t *testing.T) {
	t.Parallel()

	resources, err := ParseYAML([]byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: [a.yaml]\n---\n"+cmA), "x")
	if err != nil {
		t.Fatal(err)
	}

	if len(resources) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(resources))
	}
}

//nolint:gosec // test fixture with fake ciphertext
const sopsSecret = `apiVersion: v1
kind: Secret
metadata:
  name: s
  namespace: ns
data:
  pw: ENC[AES256_GCM,data:abc,iv:def,tag:ghi,type:str]
sops:
  mac: ENC[AES256_GCM,data:xyz,iv:1,tag:2,type:str]
  lastmodified: "2026-01-01T00:00:00Z"
  version: 3.9.0
`

func TestProcess_SOPS(t *testing.T) {
	t.Parallel()

	resources, err := ParseYAML([]byte(sopsSecret+"---\n"+cmA), "secret.yaml")
	if err != nil {
		t.Fatal(err)
	}

	if !resources[0].SOPSEncrypted || resources[1].SOPSEncrypted {
		t.Fatalf("SOPS detection wrong: %v %v", resources[0].SOPSEncrypted, resources[1].SOPSEncrypted)
	}

	kept, err := (&Options{}).Process(resources)
	if err != nil || len(kept) != 1 || kept[0].Unstructured().GetKind() != "ConfigMap" {
		t.Fatalf("expected only the ConfigMap, got %d %v", len(kept), err)
	}
}

func TestParseYAML_SOPSKeyWithoutMACIsNotEncrypted(t *testing.T) {
	t.Parallel()

	resources, err := ParseYAML([]byte(strings.Replace(cmA, "\ndata:", "\nsops:\n  note: x\ndata:", 1)), "x")
	if err != nil {
		t.Fatal(err)
	}

	if resources[0].SOPSEncrypted {
		t.Fatal("a sops key without mac must not count as encrypted")
	}
}

func TestSecretsAsStringData_KeepsBinaryInData(t *testing.T) {
	t.Parallel()

	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": "s"},
		"data": map[string]any{
			"text":   "aGVsbG8=", // hello
			"binary": "/+4AAWJpbg==",
			"broken": "not base64!",
			"both":   "eA==", // x, stringData wins
		},
		"stringData": map[string]any{"both": "y"},
	}

	err := (&Options{SecretsAsStringData: true}).Normalize(obj)
	if err != nil {
		t.Fatal(err)
	}

	stringData, _ := obj["stringData"].(map[string]any)
	if stringData["text"] != "hello" || stringData["both"] != "y" {
		t.Errorf("text not converted: %v", obj)
	}

	data, _ := obj["data"].(map[string]any)
	if data["binary"] != "/+4AAWJpbg==" || data["broken"] != "not base64!" || len(data) != 2 {
		t.Errorf("binary and broken values must stay in data: %v", data)
	}
}

func TestRelPath_StaysInOutputDir(t *testing.T) {
	t.Parallel()

	for _, ns := range []string{"..", "../../escaped", "."} {
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "ConfigMap",
			"metadata": map[string]any{"name": "..", "namespace": ns},
		}}

		rel := RelPath(u, true)
		if !filepath.IsLocal(rel) || strings.Count(rel, "/") != 2 {
			t.Errorf("namespace %q: RelPath = %q leaves the output directory", ns, rel)
		}
	}
}

func TestParseYAML_ExpandsLists(t *testing.T) {
	t.Parallel()

	list := "apiVersion: v1\nkind: List\nitems:\n- " + strings.ReplaceAll(strings.TrimSuffix(cmA, "\n"), "\n", "\n  ") + "\n- " +
		strings.ReplaceAll(strings.TrimSuffix(strings.Replace(cmA, "name: a", "name: b", 1), "\n"), "\n", "\n  ") + "\n"

	rendered := renderYAML(t, list)
	if len(rendered) != 2 || rendered["ns/ConfigMap/a.yaml"] == nil || rendered["ns/ConfigMap/b.yaml"] == nil {
		t.Fatalf("expected both items, got %v from:\n%s", rendered.sortedPaths(), list)
	}
}

func TestRender_MissingName(t *testing.T) {
	t.Parallel()

	resources, err := ParseYAML([]byte("apiVersion: v1\nkind: Pod\nmetadata:\n  generateName: p-\n"), "x")
	if err != nil {
		t.Fatal(err)
	}

	_, err = Render(resources)
	if err == nil || !strings.Contains(err.Error(), "metadata.name") {
		t.Fatalf("expected a missing name error, got %v", err)
	}
}

func TestValueMatches(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		actual, expected any
		want             bool
	}{
		{int64(600), 600, true},
		{float64(600), 600, true},
		{"600", 600, false},
		{"true", true, false},
		{"<nil>", nil, false},
		{nil, nil, true},
		{map[string]any{}, map[string]any{}, true},
		{[]any{}, []any{}, true},
		{"25%", "25%", true},
	} {
		if got := valueMatches(tc.actual, tc.expected); got != tc.want {
			t.Errorf("valueMatches(%#v, %#v) = %v, want %v", tc.actual, tc.expected, got, tc.want)
		}
	}
}

func TestRender_SanitizedNamesCollide(t *testing.T) {
	t.Parallel()

	resources, err := ParseYAML([]byte(`
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: "a:b"}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: a_b}
`), "in.yaml")
	if err != nil {
		t.Fatal(err)
	}

	_, err = Render(resources)
	if !errors.Is(err, errDuplicate) {
		t.Fatalf("expected errDuplicate, got %v", err)
	}
}

func TestWriteDir_NeverOverwrites(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rendered := Rendered{"ns/ConfigMap/a.yaml": []byte("new\n")}

	err := os.MkdirAll(filepath.Join(dir, "ns", "ConfigMap"), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(filepath.Join(dir, "ns", "ConfigMap", "a.yaml"), []byte("old\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = rendered.WriteDir(dir)
	if !errors.Is(err, errDuplicate) {
		t.Fatalf("expected errDuplicate, got %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "ns", "ConfigMap", "a.yaml"))
	if string(data) != "old\n" {
		t.Fatalf("file was overwritten: %q", data)
	}
}
