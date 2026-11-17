package main

import (
	"strings"
	"testing"

	"github.com/guettli/kubenorm/normalize"
)

const diffTestCM = `apiVersion: v1
kind: ConfigMap
metadata:
  name: a
  namespace: ns
data:
  k: v1
`

func renderYAML(t *testing.T, yamlDocs string) normalize.Rendered {
	t.Helper()

	resources, err := normalize.ParseYAML([]byte(yamlDocs), "test")
	if err != nil {
		t.Fatal(err)
	}

	rendered, err := normalize.Render(resources)
	if err != nil {
		t.Fatal(err)
	}

	return rendered
}

func TestComputeDiff(t *testing.T) {
	t.Parallel()

	oldSide := renderYAML(t, diffTestCM)
	newSide := renderYAML(t, strings.Replace(diffTestCM, "k: v1", "k: v2", 1)+"---\n"+strings.Replace(diffTestCM, "name: a", "name: b", 1))

	diffs := computeDiff(oldSide, newSide, prefixLabels("old", "new"), true)
	if len(diffs) != 2 || !strings.HasPrefix(diffs[1], "--- /dev/null") {
		t.Fatalf("unexpected diffs %+v", diffs)
	}

	out := diffs[0] + diffs[1]
	for _, want := range []string{"--- old/ns/ConfigMap/a.yaml", "+++ new/ns/ConfigMap/a.yaml", "-  k: v1", "+  k: v2", "--- /dev/null", "+++ new/ns/ConfigMap/b.yaml", "@@ -0,0 +1,"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output misses %q:\n%s", want, out)
		}
	}

	for _, full := range []bool{false, true} {
		if diffs := computeDiff(oldSide, oldSide, prefixLabels("a", "b"), full); len(diffs) != 0 {
			t.Fatalf("expected no diff, got %+v", diffs)
		}
	}
}

func TestComputeDiffOnlyIn(t *testing.T) {
	t.Parallel()

	oldSide := renderYAML(t, diffTestCM+"---\n"+strings.Replace(diffTestCM, "name: a", "name: c", 1))
	newSide := renderYAML(t, strings.Replace(diffTestCM, "k: v1", "k: v2", 1)+"---\n"+strings.Replace(diffTestCM, "name: a", "name: b", 1))

	diffs := computeDiff(oldSide, newSide, prefixLabels("old", "new"), false)

	want := []string{"Only in new/ns/ConfigMap: b.yaml\n", "Only in old/ns/ConfigMap: c.yaml\n"}
	if len(diffs) != 3 || diffs[1] != want[0] || diffs[2] != want[1] {
		t.Fatalf("unexpected diffs %q", diffs)
	}

	if !strings.Contains(diffs[0], "+  k: v2") {
		t.Errorf("modified resource misses its diff:\n%s", diffs[0])
	}
}
