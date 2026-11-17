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
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: e
  labels:
    authz.cluster.cattle.io/rtb-owner-updated: p-foo_prtb-ddphg
    authz.cluster.cattle.io/rtb-owner: 0b5c1f7e-1111-2222-3333-444455556666
    cattle.io/creator: norman
  annotations:
    field.cattle.io/creatorId: u-abc
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: f
  labels:
    authz.cluster.cattle.io/rtb-owner: 0b5c1f7e-1111-2222-3333-444455556666
    authz.cluster.cattle.io/prtb-owner-p-foo_my-binding: "true"
    authz.cluster.cattle.io/clusterrole-owner: project-member
    c-abc_crtb-x: membership-binding-owner
    cattle.io/creator: someone
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: g
  labels:
    authz.cluster.cattle.io/rtb-owner-updated: p-foo_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-9f2e1
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
		{"rancher rtb p-foo/prtb-ddphg", "rancher norman (creator u-abc)"},
		{
			"rancher roletemplate project-member", "rancher rtb uid 0b5c1f7e-1111-2222-3333-444455556666",
			"rancher prtb p-foo/my-binding", "rancher membership-binding-owner c-abc/crtb-x",
		},
		{"rancher rtb p-foo/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-9f2e1 (truncated)"},
	} {
		if got := managers(resources[i].Object); !slices.Equal(got, want) {
			t.Errorf("resource %d: got %q, want %q", i, got, want)
		}
	}
}
