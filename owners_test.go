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
  annotations:
    kubenorm.guettli.github.io/rendered-by: "foo/render.sh\nx"
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
		{"rancher rtb p-foo/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-9f2e1 (truncated)", `"rendered by foo/render.sh\nx"`},
	} {
		if got := managers(resources[i].Object, nil); !slices.Equal(got, want) {
			t.Errorf("resource %d: got %q, want %q", i, got, want)
		}
	}
}

func TestPlatformManagers(t *testing.T) {
	t.Parallel()

	resources, err := normalize.ParseYAML([]byte(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: a
  namespace: ns
  labels:
    app.kubernetes.io/managed-by: configmanagement.gke.io
  annotations:
    configmanagement.gke.io/managed: enabled
    configmanagement.gke.io/source-path: namespaces/ns/cm.yaml
    configsync.gke.io/manager: :root
    config.k8s.io/owning-inventory: config-management-system_root-sync
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: b
  namespace: operators
  labels:
    olm.owner: etcd.v0.9.4
    olm.owner.kind: ClusterServiceVersion
    olm.owner.namespace: operators
    addonmanager.kubernetes.io/mode: Reconcile
---
apiVersion: v1
kind: Secret
metadata:
  name: c
  namespace: ns
  labels:
    app.kubernetes.io/managed-by: kyverno
    generate.kyverno.io/policy-name: default-secret
  annotations:
    cert-manager.io/certificate-name: web-tls
    cert-manager.io/issuer-name: letsencrypt
    reflector.v1.k8s.emberstack.com/reflects: src/tls
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: d
  namespace: ns
  labels:
    app.kubernetes.io/managed-by: kyverno
    crossplane.io/composite: db-x7k2p
    crossplane.io/claim-name: db
    crossplane.io/claim-namespace: team
    kubed.appscode.com/origin.name: cfg
    kubed.appscode.com/origin.namespace: src
    kluctl.io/discriminator: prod
  annotations:
    resources.gardener.cloud/origin: shoot--foo/addons
    replicator.v1.mittwald.de/replicated-from-version: "17"
    kluctl.io/deployment-item-dir: apps/web
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: e
  namespace: ns
  labels:
    app.kubernetes.io/managed-by: configmanagement.gke.io
    crossplane.io/composite: db-x7k2p
    olm.owner: etcd
  annotations:
    config.k8s.io/owning-inventory: abc
    replicator.v1.mittwald.de/replicate-from: src/cfg
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: f
  namespace: ns
  labels:
    generate.kyverno.io/policy-name: p
    generate.kyverno.io/policy-namespace: team
    crossplane.io/composite: f
  annotations:
    configmanagement.gke.io/managed: enabled
    config.k8s.io/owning-inventory: abc
    replicator.v1.mittwald.de/replicated-from-source: src/f
    replicator.v1.mittwald.de/replicated-from-version: "3"
    kluctl.io/deployment-item-dir: apps
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: g
  labels:
    kluctl.io/discriminator: dev
`), "test")
	if err != nil {
		t.Fatal(err)
	}

	for i, want := range [][]string{
		{"config sync :root (file namespaces/ns/cm.yaml)"},
		{"olm ClusterServiceVersion operators/etcd.v0.9.4", "addon-manager Reconcile"},
		{"reflector copy of src/tls", "cert-manager certificate web-tls", "kyverno policy default-secret"},
		{
			"kubed copy of src/cfg", "kubernetes-replicator copy", "managed-by kyverno", "crossplane claim team/db",
			"gardener managedresource shoot--foo/addons", "kluctl prod (dir apps/web)",
		},
		{
			"kubernetes-replicator copy of src/cfg", "managed-by configmanagement.gke.io",
			"kpt/cli-utils inventory abc", "olm etcd", "crossplane composite db-x7k2p",
		},
		{"kubernetes-replicator copy of src/f", "config sync", "kyverno policy team/p", "kluctl (dir apps)"},
		{"kluctl dev"},
	} {
		if got := managers(resources[i].Object, nil); !slices.Equal(got, want) {
			t.Errorf("resource %d: got %q, want %q", i, got, want)
		}
	}
}
