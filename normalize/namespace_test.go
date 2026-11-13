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
		if got := isBuiltinClusterScoped(gk); got != want {
			t.Errorf("isBuiltinClusterScoped(%v) = %v, want %v", gk, got, want)
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

	cfg, err := parseConfig("test", []byte(`
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

	_, err := parseConfig("test", []byte(`
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

func TestExplain_MatchesProcess(t *testing.T) {
	t.Parallel()

	cfg, err := LoadConfig(true, "")
	if err != nil {
		t.Fatal(err)
	}

	opts := &Options{Config: cfg, OverwriteNamespace: "new"}

	resources, err := ParseYAML([]byte(nsTestInput+"---\napiVersion: v1\nkind: Pod\nmetadata:\n  name: p\n"), "test")
	if err != nil {
		t.Fatal(err)
	}

	explanations, err := opts.Explain(resources)
	if err != nil {
		t.Fatal(err)
	}

	resources, err = ParseYAML([]byte(nsTestInput+"---\napiVersion: v1\nkind: Pod\nmetadata:\n  name: p\n"), "test")
	if err != nil {
		t.Fatal(err)
	}

	kept, err := opts.Process(resources)
	if err != nil {
		t.Fatal(err)
	}

	keptPaths := map[string]bool{}
	for _, r := range kept {
		keptPaths[r.RelPath()] = true
	}

	for _, e := range explanations {
		if keptPaths[e.Path] == (e.Dropped != "") {
			t.Errorf("%s: explain says dropped=%q, process kept=%v", e.Path, e.Dropped, keptPaths[e.Path])
		}
	}
}

const nsRefsTestInput = `
apiVersion: v1
kind: Service
metadata:
  name: webhook
  namespace: old
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: crb
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: r}
subjects:
- {kind: ServiceAccount, name: moved, namespace: old}
- {kind: ServiceAccount, name: other, namespace: kube-system}
- {kind: ServiceAccount, name: empty}
- {kind: User, name: alice}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: rb
  namespace: old
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: r}
subjects:
- {kind: ServiceAccount, name: moved, namespace: old}
- {kind: ServiceAccount, name: own}
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: vwc
webhooks:
- name: a.example.com
  clientConfig: {service: {name: webhook, namespace: old}}
- name: b.example.com
  clientConfig: {service: {name: other, namespace: kube-system}}
- name: c.example.com
  clientConfig: {url: "https://example.com"}
---
apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata:
  name: v1.example.com
spec:
  service: {name: api}
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  scope: Namespaced
  names: {kind: Widget}
  conversion:
    strategy: Webhook
    webhook:
      clientConfig: {service: {name: webhook, namespace: old}}
`

func TestNamespaceReferences(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		opts Options
		want []string
	}{
		{
			name: "overwrite",
			opts: Options{OverwriteNamespace: "new"},
			want: []string{
				"crb subjects: moved=new other=kube-system empty=new alice=",
				"rb subjects: moved=new own=",
				"vwc services: webhook=new other=kube-system",
				"v1.example.com service: api=new",
				"widgets.example.com conversion: webhook=new",
			},
		},
		{
			name: "add if missing",
			opts: Options{AddNamespaceIfMissing: "new"},
			want: []string{
				"crb subjects: moved=old other=kube-system empty=new alice=",
				"rb subjects: moved=old own=",
				"vwc services: webhook=old other=kube-system",
				"v1.example.com service: api=new",
				"widgets.example.com conversion: webhook=old",
			},
		},
		{
			name: "no option",
			opts: Options{},
			want: []string{
				"crb subjects: moved=old other=kube-system empty= alice=",
				"rb subjects: moved=old own=",
				"vwc services: webhook=old other=kube-system",
				"v1.example.com service: api=",
				"widgets.example.com conversion: webhook=old",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resources, err := ParseYAML([]byte(nsRefsTestInput), "test")
			if err != nil {
				t.Fatal(err)
			}

			resources, err = tc.opts.Process(resources)
			if err != nil {
				t.Fatal(err)
			}

			var got []string

			refPaths := map[string]struct {
				label string
				path  []string
			}{
				"ClusterRoleBinding":             {"subjects", []string{"subjects", "name"}},
				"RoleBinding":                    {"subjects", []string{"subjects", "name"}},
				"ValidatingWebhookConfiguration": {"services", []string{"webhooks", "clientConfig", "service", "name"}},
				"APIService":                     {"service", []string{"spec", "service", "name"}},
				"CustomResourceDefinition":       {"conversion", []string{"spec", "conversion", "webhook", "clientConfig", "service", "name"}},
			}

			for _, r := range resources {
				u := r.Unstructured()

				ref, ok := refPaths[u.GetKind()]
				if !ok {
					continue
				}

				var refs []string

				visitFieldPath(u.Object, ref.path, func(m map[string]any, _ string) {
					name, _ := m["name"].(string)
					ns, _ := m["namespace"].(string)
					refs = append(refs, name+"="+ns)
				})

				label := ref.label
				got = append(got, u.GetName()+" "+label+": "+strings.Join(refs, " "))
			}

			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}
