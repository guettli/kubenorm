package normalize_test

import (
	"fmt"
	"os"

	"github.com/guettli/kubenorm/normalize"
)

// Normalize helm output with the common config and print it as one stream.
func Example() {
	input := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  labels:
    helm.sh/chart: web-1.2.3
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: web
        image: nginx:1.27
        resources:
          limits:
            cpu: 1000m
`)

	cfg, err := normalize.LoadConfig(true)
	if err != nil {
		fmt.Println(err)
		return
	}

	resources, err := normalize.ParseYAML(input, "helm-output.yaml")
	if err != nil {
		fmt.Println(err)
		return
	}

	opts := &normalize.Options{Config: cfg, AddNamespaceIfMissing: "demo"}

	resources, err = opts.Process(resources)
	if err != nil {
		fmt.Println(err)
		return
	}

	rendered, err := normalize.Render(resources)
	if err != nil {
		fmt.Println(err)
		return
	}

	err = rendered.WriteStream(os.Stdout)
	if err != nil {
		fmt.Println(err)
	}

	// Output:
	// ---
	// apiVersion: apps/v1
	// kind: Deployment
	// metadata:
	//   name: web
	//   namespace: demo
	// spec:
	//   replicas: 1
	//   template:
	//     spec:
	//       containers:
	//       - image: nginx:1.27
	//         name: web
	//         resources:
	//           limits:
	//             cpu: "1"
}
