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

func render(t *testing.T, yamlDocs string) Rendered {
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
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
}

func TestWriteStream_SortedAndSeparated(t *testing.T) {
	t.Parallel()

	rendered := render(t, strings.Replace(cmA, "name: a", "name: z", 1)+"---\n"+cmA)

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

func TestDiff(t *testing.T) {
	t.Parallel()

	oldSide := render(t, cmA)
	newSide := render(t, strings.Replace(cmA, "k: v1", "k: v2", 1)+"---\n"+strings.Replace(cmA, "name: a", "name: b", 1))

	var buf bytes.Buffer

	result, err := Diff(&buf, oldSide, newSide, "old", "new")
	if err != nil {
		t.Fatal(err)
	}

	if result.Equal() || len(result.Changed) != 1 || len(result.Added) != 1 || len(result.Removed) != 0 {
		t.Fatalf("unexpected result %+v", result)
	}

	out := buf.String()
	for _, want := range []string{"--- old/ns/ConfigMap/a.yaml", "+++ new/ns/ConfigMap/a.yaml", "-  k: v1", "+  k: v2", "--- /dev/null", "+++ new/ns/ConfigMap/b.yaml"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output misses %q:\n%s", want, out)
		}
	}

	result, err = Diff(&buf, oldSide, oldSide, "a", "b")
	if err != nil || !result.Equal() {
		t.Fatalf("expected equal, got %+v %v", result, err)
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
