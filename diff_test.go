package main

import (
	"fmt"
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

	diffs := computeDiff(oldSide, newSide, prefixLabels("old", "new"), nil)
	if len(diffs) != 2 || !strings.HasPrefix(diffs[1], "--- /dev/null") {
		t.Fatalf("unexpected diffs %+v", diffs)
	}

	out := diffs[0] + diffs[1]
	for _, want := range []string{"--- old/ns/ConfigMap/a.yaml", "+++ new/ns/ConfigMap/a.yaml", "-  k: v1", "+  k: v2", "--- /dev/null", "+++ new/ns/ConfigMap/b.yaml", "@@ -0,0 +1,"} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output misses %q:\n%s", want, out)
		}
	}

	onlyHere := func(rel string, _ bool) string { return rel }
	for _, f := range []func(string, bool) string{nil, onlyHere} {
		if diffs := computeDiff(oldSide, oldSide, prefixLabels("a", "b"), f); len(diffs) != 0 {
			t.Fatalf("expected no diff, got %+v", diffs)
		}
	}
}

func TestComputeDiffOnlyHere(t *testing.T) {
	t.Parallel()

	oldSide := renderYAML(t, diffTestCM+"---\n"+strings.Replace(diffTestCM, "name: a", "name: c", 1))
	newSide := renderYAML(t, strings.Replace(diffTestCM, "k: v1", "k: v2", 1)+"---\n"+strings.Replace(diffTestCM, "name: a", "name: b", 1))

	onlyHere := func(rel string, inOld bool) string {
		return fmt.Sprintf("%s %v\n", rel, inOld)
	}

	diffs := computeDiff(oldSide, newSide, prefixLabels("old", "new"), onlyHere)

	want := []string{"ns/ConfigMap/b.yaml false\n", "ns/ConfigMap/c.yaml true\n"}
	if len(diffs) != 3 || diffs[1] != want[0] || diffs[2] != want[1] {
		t.Fatalf("unexpected diffs %q", diffs)
	}

	if !strings.Contains(diffs[0], "+  k: v2") {
		t.Errorf("modified resource misses its diff:\n%s", diffs[0])
	}
}

func TestNewDiffSide(t *testing.T) {
	t.Parallel()

	stream := diffTestCM + "---\n" + strings.Replace(diffTestCM, "name: a", "name: b\n  labels:\n    app.kubernetes.io/managed-by: Helm\n  annotations:\n    meta.helm.sh/release-name: web\n    meta.helm.sh/release-namespace: ns", 1)

	multi, err := normalize.ParseYAML([]byte(stream), "chart.yaml")
	if err != nil {
		t.Fatal(err)
	}

	single, err := normalize.ParseYAML([]byte(strings.Replace(diffTestCM, "name: a", "name: c", 1)), "dump/ns/ConfigMap/c.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := normalize.LoadConfig(true, "")
	if err != nil {
		t.Fatal(err)
	}

	side, err := newDiffSide(append(multi, single...), &normalize.Options{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}

	for rel, want := range map[string]string{
		"ns/ConfigMap/a.yaml": "Only here (new): chart.yaml (ns/ConfigMap/a.yaml)\n",
		"ns/ConfigMap/b.yaml": "Only here (new): chart.yaml (ns/ConfigMap/b.yaml) managed by: helm release ns/web\n",
		"ns/ConfigMap/c.yaml": "Only here (new): dump/ns/ConfigMap/c.yaml\n",
	} {
		if got := onlyHereLine(side.origins[rel], "new"); got != want {
			t.Errorf("%s: got %q, want %q", rel, got, want)
		}
	}

	if strings.Contains(string(side.rendered["ns/ConfigMap/b.yaml"]), "helm") {
		t.Errorf("helm fields not normalized away:\n%s", side.rendered["ns/ConfigMap/b.yaml"])
	}
}

// TestNewDiffSide_KeepsOriginsAcrossProcess guards the assumption that
// Process normalizes the objects in place: List items, a moved namespace and
// canonicalized quantities still map back to their source.
func TestNewDiffSide_KeepsOriginsAcrossProcess(t *testing.T) {
	t.Parallel()

	resources, err := normalize.ParseYAML([]byte(`
apiVersion: v1
kind: List
items:
  - apiVersion: v1
    kind: LimitRange
    metadata:
      name: lr
      namespace: old
      labels:
        kapp.k14s.io/app: "1"
    spec:
      limits:
        - type: Container
          default:
            cpu: 1000m
`), "list.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := normalize.LoadConfig(true, "")
	if err != nil {
		t.Fatal(err)
	}

	side, err := newDiffSide(resources, &normalize.Options{Config: cfg, OverwriteNamespace: "new"})
	if err != nil {
		t.Fatal(err)
	}

	want := "Only here (new): list.yaml managed by: kapp app 1\n"
	if got := onlyHereLine(side.origins["new/LimitRange/lr.yaml"], "new"); got != want {
		t.Fatalf("got %q, want %q (origins %+v)", got, want, side.origins)
	}
}
