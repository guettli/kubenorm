package normalize

import (
	"fmt"
	"testing"
)

// widgetCRD returns a CRD whose spec.p defaults to {} and whose spec.p.a has
// the given default (or none).
func widgetCRD(t *testing.T, childDefault string) []Resource {
	t.Helper()

	resources, err := ParseYAML(fmt.Appendf(nil, `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: widgets.example.com}
spec:
  group: example.com
  names: {kind: Widget, plural: widgets}
  scope: Namespaced
  versions:
  - name: v1
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            properties:
              p:
                type: object
                default: {}
                properties:
                  a: {type: integer%s}
`, childDefault), "crd.yaml")
	if err != nil {
		t.Fatal(err)
	}

	return resources
}

// A CRD in the input replaces the same CRD given with WithCRDs (--crds):
// only the defaults of the later one are removed.
func TestCRDDefaults_LaterCRDWins(t *testing.T) {
	t.Parallel()

	widget, err := ParseYAML([]byte(`
apiVersion: example.com/v1
kind: Widget
metadata: {name: w, namespace: default}
spec:
  p: {a: 1}
`), "widget.yaml")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name     string
		old, new string
		want     int
	}{
		{"old default 1, new none: a stays", ", default: 1", "", 1},
		{"old none, new default 1: a goes", "", ", default: 1", 0},
	} {
		opts := (&Options{}).WithCRDs(widgetCRD(t, tc.old))

		kept, err := opts.Process(append(widgetCRD(t, tc.new), CloneResources(widget)...))
		if err != nil {
			t.Fatal(err)
		}

		spec, _ := kept[1].Unstructured().Object["spec"].(map[string]any)
		p, _ := spec["p"].(map[string]any)

		if len(p) != tc.want {
			t.Errorf("%s: spec = %v", tc.name, spec)
		}
	}
}
