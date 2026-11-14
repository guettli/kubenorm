package normalize

import (
	"bytes"
	"testing"
)

var fuzzSeeds = []string{
	`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  labels: {}
  creationTimestamp: null
spec:
  replicas: 1
  revisionHistoryLimit: 10
  template:
    spec:
      containers:
      - name: c
        image: nginx
        imagePullPolicy: IfNotPresent
        securityContext: {}
        resources: {limits: {cpu: 1000m, memory: 1024Mi}}
        ports: [{containerPort: 80, protocol: TCP}]
      restartPolicy: Always
`,
	`apiVersion: v1
kind: Service
metadata: {name: s, namespace: n1}
spec:
  clusterIP: None
  type: ClusterIP
  ports: [{port: 80, targetPort: 80, protocol: TCP}]
`,
	`apiVersion: batch/v1
kind: CronJob
metadata: {name: c}
spec:
  schedule: "* * * * *"
  concurrencyPolicy: Allow
  jobTemplate:
    spec:
      template:
        spec:
          containers: [{name: c, image: busybox}]
          restartPolicy: OnFailure
`,
	`apiVersion: v1
kind: List
items:
- {apiVersion: v1, kind: ConfigMap, metadata: {name: a}, data: {k: v}}
- {apiVersion: v1, kind: Secret, metadata: {name: b}, type: Opaque, data: {k: dg==}}
`,
}

// FuzzIdempotent checks that normalizing normalized output changes nothing.
func FuzzIdempotent(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}

	f.Add([]byte(quantityCRDTestInput))

	cfg, err := LoadConfig(true, "")
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		opts := &Options{Config: cfg, AddNamespaceIfMissing: "fuzz", SecretsAsStringData: true}

		first, ok := normRender(opts, data)
		if !ok {
			return // invalid input
		}

		for path, content := range first {
			second, ok := normRender(opts, content)
			if !ok {
				t.Fatalf("%s: normalized output does not parse:\n%s", path, content)
			}

			if len(second) != 1 || !bytes.Equal(second[path], content) {
				t.Fatalf("%s: not idempotent:\nfirst:\n%s\nsecond:\n%v", path, content, second)
			}
		}
	})
}

// normRender parses, processes and renders data. ok is false if any step fails.
func normRender(opts *Options, data []byte) (Rendered, bool) {
	resources, err := ParseYAML(data, "fuzz")
	if err != nil {
		return nil, false
	}

	resources, err = opts.Process(resources)
	if err != nil {
		return nil, false
	}

	rendered, err := Render(resources)
	if err != nil {
		return nil, false
	}

	return rendered, true
}
