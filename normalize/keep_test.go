package normalize

import (
	"strings"
	"testing"
)

func keepConfig(t *testing.T, keep string) *Config {
	t.Helper()

	cfg, err := LoadConfig(true)
	if err != nil {
		t.Fatal(err)
	}

	user, err := ParseConfig("keep.yaml", []byte(keep))
	if err != nil {
		t.Fatal(err)
	}

	cfg.merge(user)

	return cfg
}

func renderOne(t *testing.T, cfg *Config, input, path string) string {
	t.Helper()

	resources, err := ParseYAML([]byte(input), "test")
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

	got, ok := rendered[path]
	if !ok {
		t.Fatalf("%s not rendered", path)
	}

	return string(got)
}

func TestKeepFields(t *testing.T) {
	t.Parallel()

	cfg := keepConfig(t, `
keepFields:
  - kind: ConfigMap
    fields:
      - metadata.annotations.configmanagement\.gke\.io/token
      - status.observed*
  - fields:
      - ...containers.imagePullPolicy
`)

	got := renderOne(t, cfg, `
apiVersion: v1
kind: ConfigMap
metadata:
  name: a
  namespace: ns
  uid: "1"
  annotations:
    configmanagement.gke.io/token: 2f9c1e0d
    configsync.gke.io/declared-fields: "{}"
status:
  observedGeneration: 3
  phase: ok
`, "ns/ConfigMap/a.yaml")

	want := `apiVersion: v1
kind: ConfigMap
metadata:
  annotations:
    configmanagement.gke.io/token: 2f9c1e0d
  name: a
  namespace: ns
status:
  observedGeneration: 3
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	// The ConfigMap rule does not match a Deployment; the rule without
	// matchers keeps the defaulted imagePullPolicy in every list entry.
	got = renderOne(t, cfg, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: ns
  annotations:
    configmanagement.gke.io/token: 2f9c1e0d
spec:
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
        - name: a
          image: nginx
          imagePullPolicy: IfNotPresent
        - name: b
          image: nginx
          imagePullPolicy: IfNotPresent
          terminationMessagePath: /dev/termination-log
`, "ns/apps_Deployment/web.yaml")

	if strings.Contains(got, "token") || strings.Contains(got, "terminationMessagePath") {
		t.Errorf("removed too little:\n%s", got)
	}

	if strings.Count(got, "imagePullPolicy: IfNotPresent") != 2 {
		t.Errorf("imagePullPolicy not kept:\n%s", got)
	}
}

func TestKeepFieldsExplain(t *testing.T) {
	t.Parallel()

	cfg := keepConfig(t, `
keepFields:
  - fields:
      - metadata.uid
`)

	resources, err := ParseYAML([]byte(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: a
  namespace: ns
  uid: "1"
  resourceVersion: "2"
`), "test")
	if err != nil {
		t.Fatal(err)
	}

	explanations, err := (&Options{Config: cfg}).Explain(resources)
	if err != nil {
		t.Fatal(err)
	}

	if len(explanations) != 1 || len(explanations[0].Changes) != 1 ||
		explanations[0].Changes[0].Path != "metadata.resourceVersion" {
		t.Errorf("want only the removal of resourceVersion, got %+v", explanations)
	}
}

func TestKeepFieldsInvalid(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ config, want string }{
		{"keepFields:\n  - kind: ConfigMap\n", "invalid keepFields[1] in bad.yaml: fields must not be empty"},
		{"keepFields:\n  - fields: ['']\n", "fields must not contain empty entries"},
		{"keepFields:\n  - fields: ['metadata.[*']\n", "invalid segment glob"},
		{"keepFields:\n  - fields: [a]\n    value: 1\n", "field value not found"},
	} {
		_, err := ParseConfig("bad.yaml", []byte(tc.config))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: got %v, want %q", tc.config, err, tc.want)
		}
	}
}

func TestKeepFieldsParent(t *testing.T) {
	t.Parallel()

	// Keeping metadata.annotations keeps the map, not the annotations that
	// the common config removes; the result does not depend on siblings.
	cfg := keepConfig(t, `
keepFields:
  - fields:
      - metadata.annotations
      - ...image
removeFields:
  - kind: Pod
    fields:
      - spec.containers
`)

	for _, extra := range []string{"", "\n    other: x"} {
		got := renderOne(t, cfg, `
apiVersion: v1
kind: ConfigMap
metadata:
  name: a
  namespace: ns
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: "{}"`+extra+`
`, "ns/ConfigMap/a.yaml")
		if strings.Contains(got, "last-applied") {
			t.Errorf("last-applied-configuration kept:\n%s", got)
		}
	}

	// A removed list is not recreated for a kept field inside.
	resources, err := ParseYAML([]byte(`
apiVersion: v1
kind: Pod
metadata:
  name: p
  namespace: ns
spec:
  containers:
    - name: a
      image: nginx
`), "test")
	if err != nil {
		t.Fatal(err)
	}

	obj := resources[0].Object
	(&Options{Config: cfg}).applyRemoveRules(obj, nil)

	if _, ok := obj["spec"].(map[string]any)["containers"]; ok {
		t.Errorf("list recreated: %v", obj)
	}
}

func TestKeepFieldsWithoutCommonConfig(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfig("own.yaml", []byte(`
removeFields:
  - fields: [metadata.labels.a, metadata.labels.b]
keepFields:
  - name: keep-*
    fields: [metadata.labels.a]
`))
	if err != nil {
		t.Fatal(err)
	}

	got := renderOne(t, cfg, `
apiVersion: v1
kind: ConfigMap
metadata:
  name: keep-me
  namespace: ns
  labels: {a: "1", b: "2"}
`, "ns/ConfigMap/keep-me.yaml")
	if !strings.Contains(got, "a: \"1\"") || strings.Contains(got, "b: \"2\"") {
		t.Errorf("got:\n%s", got)
	}
}
