package main

import (
	"slices"
	"testing"

	"github.com/guettli/kubenorm/normalize"
)

func TestManagers(t *testing.T) {
	t.Parallel()

	resources, err := normalize.ParseYAML([]byte(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: a
  namespace: ns
  labels:
    app.kubernetes.io/managed-by: Helm
    argocd.argoproj.io/instance: shop
    kustomize.toolkit.fluxcd.io/name: apps
    kustomize.toolkit.fluxcd.io/namespace: flux-system
    helm.toolkit.fluxcd.io/name: web
    helm.toolkit.fluxcd.io/namespace: ns
    kapp.k14s.io/app: "123"
  annotations:
    meta.helm.sh/release-name: web
    meta.helm.sh/release-namespace: ns
    objectset.rio.cattle.io/id: default-bundle
    objectset.rio.cattle.io/owner-gvk: fleet.cattle.io/v1alpha1, Kind=BundleDeployment
    kubectl.kubernetes.io/last-applied-configuration: "{}"
  ownerReferences:
    - apiVersion: apps/v1
      kind: ReplicaSet
      name: web-1
      uid: "1"
      controller: true
    - apiVersion: v1
      kind: ConfigMap
      name: not-controller
      uid: "2"
  managedFields:
    - manager: kubectl-edit
      operation: Update
    - manager: helm
      operation: Update
    - manager: kube-controller-manager
      operation: Update
      subresource: status
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: b
  labels:
    app.kubernetes.io/managed-by: Helm
  annotations:
    argocd.argoproj.io/tracking-id: shop:/ConfigMap:ns/b
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: c
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: d
  labels:
    app.kubernetes.io/managed-by: pulumi
  annotations:
    objectset.rio.cattle.io/id: ""
    objectset.rio.cattle.io/owner-gvk: k3s.cattle.io/v1, Kind=Addon
    objectset.rio.cattle.io/owner-name: traefik
`), "test")
	if err != nil {
		t.Fatal(err)
	}

	for i, want := range [][]string{
		{
			"helm release ns/web", "argocd app shop", "flux kustomization flux-system/apps",
			"flux helmrelease ns/web", "fleet default-bundle", "kapp app 123", "kubectl apply",
			"owner ReplicaSet/web-1", "manager kubectl-edit", "manager helm",
		},
		{"helm", "argocd app shop"},
		nil,
		{"managed-by pulumi", "wrangler objectset (owner traefik)"},
	} {
		if got := managers(resources[i].Object); !slices.Equal(got, want) {
			t.Errorf("resource %d: got %q, want %q", i, got, want)
		}
	}
}
