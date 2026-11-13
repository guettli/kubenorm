package normalize

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

const nsTestInput = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: no-ns
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: in-foo
  namespace: foo
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: role
---
apiVersion: example.com/v1
kind: Widget
metadata:
  name: w
---
apiVersion: example.com/v1
kind: ClusterWidget
metadata:
  name: cw
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: clusterwidgets.example.com
spec:
  group: example.com
  scope: Cluster
  names:
    kind: ClusterWidget
`

func processNS(t *testing.T, opts *Options) map[string]string {
	t.Helper()

	resources, err := ParseYAML([]byte(nsTestInput), "test")
	if err != nil {
		t.Fatal(err)
	}

	resources, err = opts.Process(resources)
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}

	for _, r := range resources {
		u := r.Unstructured()
		got[u.GetName()] = u.GetNamespace()
	}

	return got
}

func TestIsClusterScoped(t *testing.T) {
	t.Parallel()

	for gk, want := range map[schema.GroupKind]bool{
		{Kind: "Namespace"}: true,
		{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole"}:         true,
		{Group: "apiextensions.k8s.io", Kind: "CustomResourceDefinition"}: true,
		{Group: "cert-manager.io", Kind: "ClusterIssuer"}:                 true,
		{Kind: "Pod"}:                                      false,
		{Kind: "ConfigMap"}:                                false,
		{Group: "apps", Kind: "Deployment"}:                false,
		{Group: "rbac.authorization.k8s.io", Kind: "Role"}: false,
		{Group: "example.com", Kind: "Widget"}:             false,
	} {
		if got := IsClusterScoped(gk); got != want {
			t.Errorf("IsClusterScoped(%v) = %v, want %v", gk, got, want)
		}
	}
}

func TestAddNamespaceIfMissing(t *testing.T) {
	t.Parallel()

	got := processNS(t, &Options{AddNamespaceIfMissing: "bar"})

	want := map[string]string{
		"no-ns":                      "bar",
		"in-foo":                     "foo",
		"role":                       "",
		"w":                          "bar",
		"cw":                         "", // cluster-scoped according to the CRD in the input
		"clusterwidgets.example.com": "",
	}
	assertNamespaces(t, got, want)
}

func TestOverwriteNamespace(t *testing.T) {
	t.Parallel()

	got := processNS(t, &Options{OverwriteNamespace: "bar"})

	want := map[string]string{
		"no-ns":                      "bar",
		"in-foo":                     "bar",
		"role":                       "",
		"w":                          "bar",
		"cw":                         "",
		"clusterwidgets.example.com": "",
	}
	assertNamespaces(t, got, want)
}

func TestNoNamespaceFlagsKeepsNamespaces(t *testing.T) {
	t.Parallel()

	got := processNS(t, &Options{})

	want := map[string]string{
		"no-ns":                      "",
		"in-foo":                     "foo",
		"role":                       "",
		"w":                          "",
		"cw":                         "",
		"clusterwidgets.example.com": "",
	}
	assertNamespaces(t, got, want)
}

func TestClusterScopedResourcesConfig(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfig("test", []byte(`
clusterScopedResources:
  - group: example.com
    kind: Wid*
`))
	if err != nil {
		t.Fatal(err)
	}

	got := processNS(t, &Options{Config: cfg, AddNamespaceIfMissing: "bar"})
	if got["w"] != "" {
		t.Fatalf("expected Widget to be treated as cluster-scoped, got namespace %q", got["w"])
	}

	if got["no-ns"] != "bar" {
		t.Fatalf("expected Deployment to get namespace, got %q", got["no-ns"])
	}
}

func TestClusterScopedResourcesConfig_KindRequired(t *testing.T) {
	t.Parallel()

	_, err := ParseConfig("test", []byte(`
clusterScopedResources:
  - group: example.com
`))
	if err == nil || !strings.Contains(err.Error(), "kind must be set") {
		t.Fatalf("expected kind error, got %v", err)
	}
}

func TestOverwriteNamespaceThenFilter(t *testing.T) {
	t.Parallel()

	// The namespace is injected before filtering, so --namespaces sees the
	// new value.
	got := processNS(t, &Options{OverwriteNamespace: "bar", Namespaces: map[string]struct{}{"bar": {}}})

	want := map[string]string{
		"no-ns":  "bar",
		"in-foo": "bar",
		"w":      "bar",
	}
	assertNamespaces(t, got, want)
}

func TestNamespaceInjectionChangesRelPath(t *testing.T) {
	t.Parallel()

	resources, err := ParseYAML([]byte(nsTestInput), "test")
	if err != nil {
		t.Fatal(err)
	}

	resources, err = (&Options{AddNamespaceIfMissing: "bar"}).Process(resources)
	if err != nil {
		t.Fatal(err)
	}

	rendered, err := Render(resources)
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"bar/apps_Deployment/no-ns.yaml", "_cluster/example.com_ClusterWidget/cw.yaml", "_cluster/rbac.authorization.k8s.io_ClusterRole/role.yaml"} {
		if _, ok := rendered[p]; !ok {
			t.Errorf("expected %s in %v", p, rendered.SortedPaths())
		}
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		opts    Options
		wantErr string
	}{
		{Options{}, ""},
		{Options{AddNamespaceIfMissing: "ok-ns"}, ""},
		{Options{AddNamespaceIfMissing: "a", OverwriteNamespace: "b"}, "mutually exclusive"},
		{Options{OverwriteNamespace: "Bad_NS"}, "invalid namespace"},
		{Options{AddNamespaceIfMissing: strings.Repeat("a", 64)}, "invalid namespace"},
		{Options{RedactSecrets: true, SecretsAsStringData: true}, "mutually exclusive"},
	} {
		err := tc.opts.Validate()

		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%+v: unexpected error %v", tc.opts, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%+v: expected error containing %q, got %v", tc.opts, tc.wantErr, err)
		}
	}
}

func assertNamespaces(t *testing.T, got, want map[string]string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("got %d resources %v, want %d %v", len(got), got, len(want), want)
	}

	for name, ns := range want {
		if got[name] != ns {
			t.Errorf("%s: namespace %q, want %q", name, got[name], ns)
		}
	}
}

const refsInput = `
apiVersion: v1
kind: ServiceAccount
metadata:
  name: app
  namespace: old
---
apiVersion: v1
kind: Service
metadata:
  name: webhook
  namespace: old
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: app
subjects:
- kind: ServiceAccount
  name: app
  namespace: old
- kind: ServiceAccount
  name: foreign
  namespace: old
- kind: ServiceAccount
  name: app
  namespace: other
- kind: User
  name: app
  namespace: old
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
      namespace: old
- name: b.example.com
  clientConfig:
    url: https://example.com
---
apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata:
  name: v1.example.com
spec:
  service:
    name: webhook
    namespace: old
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  scope: Namespaced
  names:
    kind: Widget
  conversion:
    strategy: Webhook
    webhook:
      clientConfig:
        service:
          name: webhook
          namespace: old
`

func processRefs(t *testing.T, opts *Options) Rendered {
	t.Helper()

	resources, err := ParseYAML([]byte(refsInput), "test")
	if err != nil {
		t.Fatal(err)
	}

	resources, err = opts.Process(resources)
	if err != nil {
		t.Fatal(err)
	}

	rendered, err := Render(resources)
	if err != nil {
		t.Fatal(err)
	}

	return rendered
}

func TestOverwriteNamespace_UpdatesReferences(t *testing.T) {
	t.Parallel()

	rendered := processRefs(t, &Options{OverwriteNamespace: "new"})

	crb := string(rendered["_cluster/rbac.authorization.k8s.io_ClusterRoleBinding/app.yaml"])
	for _, want := range []string{
		"- kind: ServiceAccount\n  name: app\n  namespace: new\n",
		"- kind: ServiceAccount\n  name: foreign\n  namespace: old\n", // not in the input
		"- kind: ServiceAccount\n  name: app\n  namespace: other\n",   // other namespace
		"- kind: User\n  name: app\n  namespace: old\n",               // not a ServiceAccount
	} {
		assertContains(t, crb, want)
	}

	for _, p := range []string{
		"_cluster/admissionregistration.k8s.io_ValidatingWebhookConfiguration/hook.yaml",
		"_cluster/apiregistration.k8s.io_APIService/v1.example.com.yaml",
		"_cluster/apiextensions.k8s.io_CustomResourceDefinition/widgets.example.com.yaml",
	} {
		content := string(rendered[p])
		assertContains(t, content, "name: webhook\n")
		assertContains(t, content, "namespace: new\n")

		if strings.Contains(content, "namespace: old") {
			t.Errorf("%s still references the old namespace:\n%s", p, content)
		}
	}
}

func TestOverwriteNamespace_KeepReferences(t *testing.T) {
	t.Parallel()

	rendered := processRefs(t, &Options{OverwriteNamespace: "new", KeepNamespaceReferences: true})

	assertContains(t, string(rendered["_cluster/apiregistration.k8s.io_APIService/v1.example.com.yaml"]), "namespace: old\n")
	assertContains(t, string(rendered["new/ServiceAccount/app.yaml"]), "namespace: new\n")
}

func TestAddNamespaceIfMissing_UpdatesEmptyReferences(t *testing.T) {
	t.Parallel()

	input := `
apiVersion: v1
kind: Service
metadata:
  name: webhook
---
apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata:
  name: v1.example.com
spec:
  service:
    name: webhook
`

	resources, err := ParseYAML([]byte(input), "test")
	if err != nil {
		t.Fatal(err)
	}

	resources, err = (&Options{AddNamespaceIfMissing: "liqo"}).Process(resources)
	if err != nil {
		t.Fatal(err)
	}

	rendered, err := Render(resources)
	if err != nil {
		t.Fatal(err)
	}

	assertContains(t, string(rendered["_cluster/apiregistration.k8s.io_APIService/v1.example.com.yaml"]), "name: webhook\n    namespace: liqo\n")
	assertContains(t, string(rendered["liqo/Service/webhook.yaml"]), "namespace: liqo\n")
}
