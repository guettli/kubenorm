package normalize

import (
	"bytes"
	"errors"
	"strings"
	"testing"
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

func TestComputeDiff(t *testing.T) {
	t.Parallel()

	oldSide := renderYAML(t, cmA)
	newSide := renderYAML(t, strings.Replace(cmA, "k: v1", "k: v2", 1)+"---\n"+strings.Replace(cmA, "name: a", "name: b", 1))

	diffs := ComputeDiff(oldSide, newSide, "old", "new")
	if len(diffs) != 2 || diffs[0].Status != StatusModified || diffs[1].Status != StatusAdded {
		t.Fatalf("unexpected diffs %+v", diffs)
	}

	out := diffs[0].Unified + diffs[1].Unified
	for _, want := range []string{"--- old/ns/ConfigMap/a.yaml", "+++ new/ns/ConfigMap/a.yaml", "-  k: v1", "+  k: v2", "--- /dev/null", "+++ new/ns/ConfigMap/b.yaml", "@@ -0,0 +1,"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output misses %q:\n%s", want, out)
		}
	}

	if diffs := ComputeDiff(oldSide, oldSide, "a", "b"); len(diffs) != 0 {
		t.Fatalf("expected no diff, got %+v", diffs)
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
