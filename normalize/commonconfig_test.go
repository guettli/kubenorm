package normalize

// Tests of the rules of the embedded common config (common-config.yaml)
// that are not about quantities (see quantity_test.go).

import (
	"strings"
	"testing"
)

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

func TestCommonConfig_RestartTimestamps(t *testing.T) {
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
  annotations:
    cattle.io/timestamp: "2026-11-12T13:55:13Z"
spec:
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
      annotations:
        cattle.io/timestamp: "2026-11-12T13:55:13Z"
        kubectl.kubernetes.io/restartedAt: "2026-11-12T13:55:13Z"
        keel.sh/update-time: "2026-11-12T13:55:13Z"
        example.com/keep: "yes"
    spec:
      containers:
        - name: web
          image: nginx
---
apiVersion: batch/v1
kind: CronJob
metadata:
  name: job
  namespace: demo
spec:
  schedule: "0 * * * *"
  jobTemplate:
    spec:
      template:
        metadata:
          annotations:
            cattle.io/timestamp: "2026-11-12T13:55:13Z"
        spec:
          restartPolicy: Never
          containers:
            - name: job
              image: busybox
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

	got := string(rendered["demo/apps_Deployment/web.yaml"])
	for _, gone := range []string{"cattle.io/timestamp", "restartedAt", "keel.sh/update-time"} {
		if strings.Contains(got, gone) {
			t.Errorf("%s not removed:\n%s", gone, got)
		}
	}

	// The only template annotation left is the user's one.
	if !strings.Contains(got, "      annotations:\n        example.com/keep: \"yes\"\n      labels:") {
		t.Errorf("unexpected template annotations:\n%s", got)
	}

	// Top-level annotations map is gone, not left empty.
	if strings.Contains(got, "metadata:\n  annotations:") {
		t.Errorf("empty annotations kept:\n%s", got)
	}

	cronJob := string(rendered["demo/batch_CronJob/job.yaml"])
	if cronJob == "" || strings.Contains(cronJob, "cattle.io/timestamp") || strings.Contains(cronJob, "annotations") {
		t.Errorf("CronJob template annotation not removed:\n%s", cronJob)
	}
}

func TestCommonConfig_RestartTimestampsKeptInCustomResources(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(true, "")
	if err != nil {
		t.Fatal(err)
	}

	resources, err := ParseYAML([]byte(`
apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: web
  namespace: demo
spec:
  values:
    annotations:
      kubectl.kubernetes.io/restartedAt: "2026-11-12T13:55:13Z"
      keel.sh/update-time: "2026-11-12T13:55:13Z"
      cattle.io/timestamp: "2026-11-12T13:55:13Z"
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

	got := string(rendered["demo/helm.toolkit.fluxcd.io_HelmRelease/web.yaml"])
	for _, kept := range []string{"restartedAt", "keel.sh/update-time", "cattle.io/timestamp"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%s removed from the values of a custom resource:\n%s", kept, got)
		}
	}
}
