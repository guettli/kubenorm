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

	diffs := ComputeDiff(oldSide, newSide, PrefixLabels("old", "new"))
	if len(diffs) != 2 || diffs[0].Status != statusModified || diffs[1].Status != statusAdded {
		t.Fatalf("unexpected diffs %+v", diffs)
	}

	out := diffs[0].Unified + diffs[1].Unified
	for _, want := range []string{"--- old/ns/ConfigMap/a.yaml", "+++ new/ns/ConfigMap/a.yaml", "-  k: v1", "+  k: v2", "--- /dev/null", "+++ new/ns/ConfigMap/b.yaml", "@@ -0,0 +1,"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output misses %q:\n%s", want, out)
		}
	}

	if diffs := ComputeDiff(oldSide, oldSide, PrefixLabels("a", "b")); len(diffs) != 0 {
		t.Fatalf("expected no diff, got %+v", diffs)
	}
}
