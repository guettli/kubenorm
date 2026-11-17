package normalize

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCanonicalQuantity(t *testing.T) {
	t.Parallel()

	for in, want := range map[any]string{
		"1000m":     "1",
		"100m":      "100m",
		"0.5":       "500m",
		0.25:        "250m",
		"1024Mi":    "1Gi",
		"1Gi":       "1Gi",
		"1e3":       "1e3",
		"1000":      "1k",
		int64(2):    "2",
		2:           "2",
		"128974848": "128974848",
		"129e6":     "129e6",
		"500M":      "500M",
	} {
		got, ok := canonicalQuantity(in)
		if !ok || got != want {
			t.Errorf("canonicalQuantity(%#v) = %q, %v; want %q", in, got, ok, want)
		}
	}

	for _, in := range []any{"abc", "", true, nil, map[string]any{}} {
		if _, ok := canonicalQuantity(in); ok {
			t.Errorf("canonicalQuantity(%#v) should not be a quantity", in)
		}
	}
}

func TestCommonConfig_CanonicalizesQuantities(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(true, "")
	if err != nil {
		t.Fatal(err)
	}

	resources, err := ParseYAML([]byte(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: demo
spec:
  replicas: 3
  template:
    spec:
      containers:
      - name: web
        resources:
          limits:
            cpu: 1000m
            memory: 1024Mi
            nvidia.com/gpu: 1
          requests:
            cpu: 0.5
      volumes:
      - name: tmp
        emptyDir:
          sizeLimit: 1024Mi
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: data
  namespace: demo
spec:
  resources:
    requests:
      storage: 10240Mi
---
apiVersion: example.com/v1
kind: Widget
metadata:
  name: w
  namespace: demo
spec:
  resources:
    limits:
      cpu: 1000m
`), "test")
	if err != nil {
		t.Fatal(err)
	}

	resources, err = (&Options{Config: cfg}).Process(resources)
	if err != nil {
		t.Fatal(err)
	}

	rendered, err := Render(resources)
	if err != nil {
		t.Fatal(err)
	}

	deploy := string(rendered["demo/apps_Deployment/web.yaml"])
	for _, want := range []string{"cpu: \"1\"\n", "memory: 1Gi\n", "nvidia.com/gpu: \"1\"\n", "cpu: 500m\n", "sizeLimit: 1Gi\n", "replicas: 3\n"} {
		assertContains(t, deploy, want)
	}

	assertContains(t, string(rendered["demo/PersistentVolumeClaim/data.yaml"]), "storage: 10Gi\n")

	// Unknown CRDs are not touched: their fields may not be quantities.
	assertContains(t, string(rendered["demo/example.com_Widget/w.yaml"]), "cpu: 1000m\n")
}

func TestQuantityFields_UserConfig(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfig("test", []byte(`
quantityFields:
  - group: example.com
    kind: Widget
    fields:
      - spec.size
`))
	if err != nil {
		t.Fatal(err)
	}

	resources, err := ParseYAML([]byte("apiVersion: example.com/v1\nkind: Widget\nmetadata:\n  name: w\nspec:\n  size: 2048Mi\n"), "test")
	if err != nil {
		t.Fatal(err)
	}

	resources, err = (&Options{Config: cfg}).Process(resources)
	if err != nil {
		t.Fatal(err)
	}

	rendered, err := Render(resources)
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, string(rendered["_cluster/example.com_Widget/w.yaml"]), "size: 2Gi\n")
}

func assertContains(t *testing.T, s, want string) {
	t.Helper()

	if !strings.Contains(s, want) {
		t.Errorf("missing %q in:\n%s", want, s)
	}
}

func TestQuantityFields_FromCRD(t *testing.T) {
	t.Parallel()

	resources, err := ParseYAML([]byte(quantityCRDTestInput), "test")
	if err != nil {
		t.Fatal(err)
	}

	resources, err = (&Options{}).Process(resources)
	if err != nil {
		t.Fatal(err)
	}

	rendered, err := Render(resources)
	if err != nil {
		t.Fatal(err)
	}

	got := string(rendered["ns1/example.com_Widget/w.yaml"])
	for _, want := range []string{"size: 2Gi\n", `cpu: "1"` + "\n", "capacity: 512Mi\n", `port: "1000"` + "\n"} {
		assertContains(t, got, want)
	}
}

func TestWithCRDs(t *testing.T) {
	t.Parallel()

	docs := strings.SplitN(quantityCRDTestInput, "---\n", 2)

	crds, err := ParseYAML([]byte(docs[0]), "crds.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cr := func() map[string]any {
		resources, err := ParseYAML([]byte(docs[1]), "cr.yaml")
		if err != nil {
			t.Fatal(err)
		}

		return resources[0].Object
	}

	opts := (&Options{}).WithCRDs(crds)

	obj := cr()

	err = opts.Normalize(obj)
	if err != nil {
		t.Fatal(err)
	}

	size, _, _ := unstructured.NestedString(obj, "spec", "size")
	if size != "2Gi" {
		t.Fatalf("Normalize: size = %q, want 2Gi", size)
	}

	// The receiver is not modified.
	obj = cr()

	err = (&Options{}).Normalize(obj)
	if err != nil {
		t.Fatal(err)
	}

	size, _, _ = unstructured.NestedString(obj, "spec", "size")
	if size != "2048Mi" {
		t.Fatalf("Options without CRDs: size = %q, want 2048Mi", size)
	}
}

func TestWithCRDs_Scope(t *testing.T) {
	t.Parallel()

	crds, err := ParseYAML([]byte(`
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: gadgets.example.com}
spec:
  group: example.com
  scope: Cluster
  names: {kind: Gadget}
`), "crds.yaml")
	if err != nil {
		t.Fatal(err)
	}

	resources, err := ParseYAML([]byte("apiVersion: example.com/v1\nkind: Gadget\nmetadata: {name: g}\n"), "in.yaml")
	if err != nil {
		t.Fatal(err)
	}

	resources, err = (&Options{AddNamespaceIfMissing: "ns1"}).WithCRDs(crds).Process(resources)
	if err != nil {
		t.Fatal(err)
	}

	if ns := resources[0].Unstructured().GetNamespace(); ns != "" {
		t.Fatalf("cluster-scoped Gadget got namespace %q", ns)
	}
}
