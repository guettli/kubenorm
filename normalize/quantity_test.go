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

	cfg, err := parseConfig("test", []byte(`
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

func TestCommonConfig_CronJobDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(true, "")
	if err != nil {
		t.Fatal(err)
	}

	// As returned by the api-server for a minimal CronJob.
	resources, err := ParseYAML([]byte(`
apiVersion: batch/v1
kind: CronJob
metadata:
  name: prune
  namespace: ops
  annotations:
    meta.helm.sh/release-name: ops
    meta.helm.sh/release-namespace: ops
spec:
  concurrencyPolicy: Allow
  failedJobsHistoryLimit: 1
  schedule: "0 * * * *"
  successfulJobsHistoryLimit: 3
  suspend: false
  jobTemplate:
    metadata:
      creationTimestamp: null
    spec:
      template:
        metadata:
          creationTimestamp: null
        spec:
          containers:
          - image: busybox:1.36
            imagePullPolicy: IfNotPresent
            name: prune
            resources: {}
            terminationMessagePath: /dev/termination-log
            terminationMessagePolicy: File
          dnsPolicy: ClusterFirst
          restartPolicy: OnFailure
          schedulerName: default-scheduler
          securityContext: {}
          terminationGracePeriodSeconds: 30
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

	want := `apiVersion: batch/v1
kind: CronJob
metadata:
  name: prune
  namespace: ops
spec:
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - image: busybox:1.36
            name: prune
          restartPolicy: OnFailure
  schedule: 0 * * * *
`
	if got := string(rendered["ops/batch_CronJob/prune.yaml"]); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCommonConfig_KeepsExplicitValues(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(true, "")
	if err != nil {
		t.Fatal(err)
	}

	resources, err := ParseYAML([]byte(`
apiVersion: v1
kind: Service
metadata:
  name: headless
  namespace: ns
spec:
  clusterIP: None
  clusterIPs: [None]
---
apiVersion: v1
kind: Service
metadata:
  name: normal
  namespace: ns
spec:
  clusterIP: 10.43.0.7
  clusterIPs: [10.43.0.7]
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: hook
webhooks:
- name: a.example.com
  clientConfig:
    service:
      name: webhook
      namespace: ns
      port: 9443
  timeoutSeconds: 10
  objectSelector:
    matchLabels:
      skip: "false"
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: db
  namespace: ns
spec:
  template:
    spec:
      containers:
      - name: db
        image: db:latest
        imagePullPolicy: Always
        resources:
          limits: {}
      securityContext:
        sysctls: []
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

	assertContains(t, string(rendered["ns/Service/headless.yaml"]), "clusterIP: None\n")
	assertContains(t, string(rendered["ns/Service/headless.yaml"]), "clusterIPs:\n  - None\n")

	if strings.Contains(string(rendered["ns/Service/normal.yaml"]), "10.43.0.7") {
		t.Errorf("allocated clusterIP must be removed:\n%s", rendered["ns/Service/normal.yaml"])
	}

	hook := string(rendered["_cluster/admissionregistration.k8s.io_ValidatingWebhookConfiguration/hook.yaml"])
	assertContains(t, hook, "port: 9443\n")
	assertContains(t, hook, "skip: \"false\"\n")

	if strings.Contains(hook, "timeoutSeconds") {
		t.Errorf("default timeoutSeconds must be removed:\n%s", hook)
	}

	sts := string(rendered["ns/apps_StatefulSet/db.yaml"])
	assertContains(t, sts, "imagePullPolicy: Always\n")

	if strings.Contains(sts, "securityContext") || strings.Contains(sts, "resources") {
		t.Errorf("emptied securityContext/resources must be removed:\n%s", sts)
	}
}

const quantityCRDTestInput = `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  scope: Namespaced
  names: {kind: Widget}
  versions:
  - name: v1
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            properties:
              size: &quantity
                anyOf: [{type: integer}, {type: string}]
                pattern: ^(\+|-)?(([0-9]+(\.[0-9]*)?)|(\.[0-9]+))(([KMGTPE]i)|[numkMGTPE]|([eE](\+|-)?(([0-9]+(\.[0-9]*)?)|(\.[0-9]+))))?$
                x-kubernetes-int-or-string: true
              limits:
                type: object
                additionalProperties: *quantity
              disks:
                type: array
                items:
                  type: object
                  properties:
                    capacity: *quantity
              port:
                anyOf: [{type: integer}, {type: string}]
                x-kubernetes-int-or-string: true
---
apiVersion: example.com/v1
kind: Widget
metadata: {name: w, namespace: ns1}
spec:
  size: 2048Mi
  limits: {cpu: 1000m}
  disks: [{capacity: 0.5Gi}]
  port: "1000"
`

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

func TestCommonConfig_GitOpsTrackingFields(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(true, "")
	if err != nil {
		t.Fatal(err)
	}

	resources, err := ParseYAML([]byte(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: app
  namespace: demo
  labels:
    app: web
    argocd.argoproj.io/instance: demo
    helm.toolkit.fluxcd.io/name: app
    helm.toolkit.fluxcd.io/namespace: demo
    kustomize.toolkit.fluxcd.io/name: apps
    kustomize.toolkit.fluxcd.io/namespace: flux-system
  annotations:
    argocd.argoproj.io/tracking-id: demo:/ConfigMap:demo/app
    kustomize.toolkit.fluxcd.io/prune: disabled
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

	want := `apiVersion: v1
kind: ConfigMap
metadata:
  annotations:
    kustomize.toolkit.fluxcd.io/prune: disabled
  labels:
    app: web
  name: app
  namespace: demo
`
	if got := string(rendered["demo/ConfigMap/app.yaml"]); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
