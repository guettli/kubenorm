package normalize

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// builtinClusterScoped lists group/kind pairs that are cluster-scoped.
//
// Without an api-server there is no discovery. This list, CRDs found in the
// input (spec.scope) and the clusterScopedResources config section decide
// whether a resource may get a namespace injected by AddNamespaceIfMissing or
// OverwriteNamespace. Everything else is treated as namespaced.
//
// Only add entries whose scope is certain: a wrong entry silently prevents a
// namespace from being set.
var builtinClusterScoped = map[schema.GroupKind]struct{}{
	// Kubernetes built-ins (as of 1.34).
	{Group: "", Kind: "Namespace"}:                                                    {},
	{Group: "", Kind: "Node"}:                                                         {},
	{Group: "", Kind: "PersistentVolume"}:                                             {},
	{Group: "", Kind: "ComponentStatus"}:                                              {},
	{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole"}:                         {},
	{Group: "rbac.authorization.k8s.io", Kind: "ClusterRoleBinding"}:                  {},
	{Group: "apiextensions.k8s.io", Kind: "CustomResourceDefinition"}:                 {},
	{Group: "apiregistration.k8s.io", Kind: "APIService"}:                             {},
	{Group: "admissionregistration.k8s.io", Kind: "MutatingWebhookConfiguration"}:     {},
	{Group: "admissionregistration.k8s.io", Kind: "ValidatingWebhookConfiguration"}:   {},
	{Group: "admissionregistration.k8s.io", Kind: "ValidatingAdmissionPolicy"}:        {},
	{Group: "admissionregistration.k8s.io", Kind: "ValidatingAdmissionPolicyBinding"}: {},
	{Group: "admissionregistration.k8s.io", Kind: "MutatingAdmissionPolicy"}:          {},
	{Group: "admissionregistration.k8s.io", Kind: "MutatingAdmissionPolicyBinding"}:   {},
	{Group: "storage.k8s.io", Kind: "StorageClass"}:                                   {},
	{Group: "storage.k8s.io", Kind: "VolumeAttachment"}:                               {},
	{Group: "storage.k8s.io", Kind: "CSIDriver"}:                                      {},
	{Group: "storage.k8s.io", Kind: "CSINode"}:                                        {},
	{Group: "storage.k8s.io", Kind: "VolumeAttributesClass"}:                          {},
	{Group: "certificates.k8s.io", Kind: "CertificateSigningRequest"}:                 {},
	{Group: "certificates.k8s.io", Kind: "ClusterTrustBundle"}:                        {},
	{Group: "scheduling.k8s.io", Kind: "PriorityClass"}:                               {},
	{Group: "node.k8s.io", Kind: "RuntimeClass"}:                                      {},
	{Group: "flowcontrol.apiserver.k8s.io", Kind: "FlowSchema"}:                       {},
	{Group: "flowcontrol.apiserver.k8s.io", Kind: "PriorityLevelConfiguration"}:       {},
	{Group: "networking.k8s.io", Kind: "IngressClass"}:                                {},
	{Group: "networking.k8s.io", Kind: "IPAddress"}:                                   {},
	{Group: "networking.k8s.io", Kind: "ServiceCIDR"}:                                 {},
	{Group: "resource.k8s.io", Kind: "DeviceClass"}:                                   {},
	{Group: "resource.k8s.io", Kind: "ResourceSlice"}:                                 {},
	{Group: "storagemigration.k8s.io", Kind: "StorageVersionMigration"}:               {},
	{Group: "internal.apiserver.k8s.io", Kind: "StorageVersion"}:                      {},
	{Group: "policy", Kind: "PodSecurityPolicy"}:                                      {},
	{Group: "authentication.k8s.io", Kind: "TokenReview"}:                             {},
	{Group: "authentication.k8s.io", Kind: "SelfSubjectReview"}:                       {},
	{Group: "authorization.k8s.io", Kind: "SelfSubjectAccessReview"}:                  {},
	{Group: "authorization.k8s.io", Kind: "SelfSubjectRulesReview"}:                   {},
	{Group: "authorization.k8s.io", Kind: "SubjectAccessReview"}:                      {},
	{Group: "metrics.k8s.io", Kind: "NodeMetrics"}:                                    {},

	// Widely used ecosystem CRDs.
	{Group: "cert-manager.io", Kind: "ClusterIssuer"}:                       {},
	{Group: "trust.cert-manager.io", Kind: "Bundle"}:                        {},
	{Group: "gateway.networking.k8s.io", Kind: "GatewayClass"}:              {},
	{Group: "policy.networking.k8s.io", Kind: "AdminNetworkPolicy"}:         {},
	{Group: "policy.networking.k8s.io", Kind: "BaselineAdminNetworkPolicy"}: {},
	{Group: "snapshot.storage.k8s.io", Kind: "VolumeSnapshotClass"}:         {},
	{Group: "snapshot.storage.k8s.io", Kind: "VolumeSnapshotContent"}:       {},
	{Group: "kyverno.io", Kind: "ClusterPolicy"}:                            {},
	{Group: "kyverno.io", Kind: "ClusterCleanupPolicy"}:                     {},
	{Group: "templates.gatekeeper.sh", Kind: "ConstraintTemplate"}:          {},
	{Group: "external-secrets.io", Kind: "ClusterSecretStore"}:              {},
	{Group: "external-secrets.io", Kind: "ClusterExternalSecret"}:           {},
	{Group: "cilium.io", Kind: "CiliumClusterwideNetworkPolicy"}:            {},
	{Group: "cilium.io", Kind: "CiliumNode"}:                                {},
	{Group: "cilium.io", Kind: "CiliumIdentity"}:                            {},
	{Group: "cilium.io", Kind: "CiliumLoadBalancerIPPool"}:                  {},
	{Group: "crd.projectcalico.org", Kind: "GlobalNetworkPolicy"}:           {},
	{Group: "crd.projectcalico.org", Kind: "GlobalNetworkSet"}:              {},
	{Group: "crd.projectcalico.org", Kind: "IPPool"}:                        {},
	{Group: "crd.projectcalico.org", Kind: "FelixConfiguration"}:            {},
	{Group: "crd.projectcalico.org", Kind: "BGPConfiguration"}:              {},
	{Group: "crd.projectcalico.org", Kind: "BGPPeer"}:                       {},
	{Group: "crd.projectcalico.org", Kind: "HostEndpoint"}:                  {},
	{Group: "karpenter.sh", Kind: "NodePool"}:                               {},
	{Group: "karpenter.sh", Kind: "NodeClaim"}:                              {},
	{Group: "argoproj.io", Kind: "ClusterWorkflowTemplate"}:                 {},
	{Group: "keda.sh", Kind: "ClusterTriggerAuthentication"}:                {},
	{Group: "kueue.x-k8s.io", Kind: "ClusterQueue"}:                         {},
	{Group: "kueue.x-k8s.io", Kind: "ResourceFlavor"}:                       {},
	{Group: "security.openshift.io", Kind: "SecurityContextConstraints"}:    {},
}

// groupKindFile is a group/kind glob pair in the clusterScopedResources
// config section.
type groupKindFile struct {
	Group *string `yaml:"group"`
	Kind  *string `yaml:"kind"`
}

type groupKindPattern struct {
	GroupPattern string
	KindPattern  string
}

func compileGroupKindPatterns(name string, entries []groupKindFile) ([]groupKindPattern, error) {
	out := make([]groupKindPattern, 0, len(entries))

	for idx, entry := range entries {
		if entry.Kind == nil || strings.TrimSpace(*entry.Kind) == "" {
			return nil, fmt.Errorf("invalid clusterScopedResources entry %d in %s: kind must be set", idx+1, name)
		}

		// group: "" means the core group, so an omitted group is a wildcard
		// but an explicit empty string is kept.
		groupPattern := "*"
		if entry.Group != nil {
			groupPattern = strings.TrimSpace(*entry.Group)
		}

		kindPattern := strings.TrimSpace(*entry.Kind)

		for _, p := range []string{groupPattern, kindPattern} {
			err := validateGlobPattern(p)
			if err != nil {
				return nil, fmt.Errorf("invalid clusterScopedResources entry %d in %s: invalid glob %q: %w", idx+1, name, p, err)
			}
		}

		out = append(out, groupKindPattern{GroupPattern: groupPattern, KindPattern: kindPattern})
	}

	return out, nil
}

// scopeResolver decides whether a group/kind is cluster-scoped.
type scopeResolver struct {
	// fromCRDs holds scopes declared by CRDs in the input: true = cluster.
	fromCRDs map[schema.GroupKind]bool
	patterns []groupKindPattern
}

// scopeResolver builds a resolver from the config and all CRDs in resources.
func (o *Options) scopeResolver(resources []Resource) *scopeResolver {
	s := &scopeResolver{fromCRDs: map[schema.GroupKind]bool{}}
	if o.Config != nil {
		s.patterns = o.Config.clusterScopedPattern
	}

	for _, r := range resources {
		u := r.Unstructured()

		gvk := u.GroupVersionKind()
		if gvk.Group != "apiextensions.k8s.io" || gvk.Kind != "CustomResourceDefinition" {
			continue
		}

		group, _, _ := unstructured.NestedString(u.Object, "spec", "group")
		kind, _, _ := unstructured.NestedString(u.Object, "spec", "names", "kind")
		scope, _, _ := unstructured.NestedString(u.Object, "spec", "scope")

		if kind == "" || scope == "" {
			continue
		}

		s.fromCRDs[schema.GroupKind{Group: group, Kind: kind}] = scope == "Cluster"
	}

	return s
}

// IsClusterScoped reports whether the built-in list knows gk as
// cluster-scoped.
func IsClusterScoped(gk schema.GroupKind) bool {
	_, ok := builtinClusterScoped[gk]
	return ok
}

func (s *scopeResolver) isClusterScoped(gk schema.GroupKind) bool {
	if cluster, ok := s.fromCRDs[gk]; ok {
		return cluster
	}

	for _, p := range s.patterns {
		if matchGlob(p.GroupPattern, gk.Group) && matchGlob(p.KindPattern, gk.Kind) {
			return true
		}
	}

	return IsClusterScoped(gk)
}

var dns1123LabelRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ValidateNamespaceName checks that ns is a valid RFC 1123 label.
func ValidateNamespaceName(ns string) error {
	if len(ns) > 63 || !dns1123LabelRegex.MatchString(ns) {
		return fmt.Errorf("invalid namespace %q: must be a lowercase RFC 1123 label (a-z, 0-9, '-', max 63 chars)", ns)
	}

	return nil
}

// Validate checks option combinations.
func (o *Options) Validate() error {
	if o.AddNamespaceIfMissing != "" && o.OverwriteNamespace != "" {
		return errors.New("add-namespace-if-missing and overwrite-namespace are mutually exclusive")
	}

	for _, ns := range []string{o.AddNamespaceIfMissing, o.OverwriteNamespace} {
		if ns == "" {
			continue
		}

		err := ValidateNamespaceName(ns)
		if err != nil {
			return err
		}
	}

	if o.SecretsAsStringData && o.RedactSecrets {
		return errors.New("secrets-as-stringdata and redact-secrets are mutually exclusive")
	}

	return nil
}

// applyNamespace sets metadata.namespace according to AddNamespaceIfMissing
// or OverwriteNamespace and records the change in moves. Cluster-scoped
// resources are never modified; a cluster-scoped resource that carries a
// namespace is left as is.
func (o *Options) applyNamespace(u *unstructured.Unstructured, scope *scopeResolver, moves namespaceMoves) {
	if o.AddNamespaceIfMissing == "" && o.OverwriteNamespace == "" {
		return
	}

	if scope.isClusterScoped(u.GroupVersionKind().GroupKind()) {
		return
	}

	oldNamespace := u.GetNamespace()

	switch {
	case o.OverwriteNamespace != "":
		u.SetNamespace(o.OverwriteNamespace)
	case oldNamespace == "":
		u.SetNamespace(o.AddNamespaceIfMissing)
	}

	if u.GetNamespace() != oldNamespace {
		moves[movedObject{kind: u.GetKind(), namespace: oldNamespace, name: u.GetName()}] = u.GetNamespace()
	}
}

type movedObject struct {
	kind      string
	namespace string
	name      string
}

// namespaceMoves maps an object (with its old namespace) to its new namespace.
type namespaceMoves map[movedObject]string

// updateReferences rewrites namespace references to moved objects, like the
// namespace transformer of kustomize:
//   - subjects of kind ServiceAccount in RoleBindings and ClusterRoleBindings
//   - service references of webhook configurations, APIServices and CRD
//     conversion webhooks
//
// A reference is only changed if the referenced object is part of the input
// and was moved.
func (m namespaceMoves) updateReferences(obj map[string]any) {
	if len(m) == 0 {
		return
	}

	u := &unstructured.Unstructured{Object: obj}
	gvk := u.GroupVersionKind()

	switch {
	case gvk.Group == "rbac.authorization.k8s.io" && (gvk.Kind == "RoleBinding" || gvk.Kind == "ClusterRoleBinding"):
		subjects, _ := obj["subjects"].([]any)
		for _, s := range subjects {
			subject, ok := s.(map[string]any)
			if ok && subject["kind"] == "ServiceAccount" {
				m.update(subject, "ServiceAccount")
			}
		}
	case gvk.Group == "admissionregistration.k8s.io" && (gvk.Kind == "MutatingWebhookConfiguration" || gvk.Kind == "ValidatingWebhookConfiguration"):
		webhooks, _ := obj["webhooks"].([]any)
		for _, w := range webhooks {
			webhook, ok := w.(map[string]any)
			if !ok {
				continue
			}

			if service, ok := nestedMap(webhook, "clientConfig", "service"); ok {
				m.update(service, "Service")
			}
		}
	case gvk.Group == "apiregistration.k8s.io" && gvk.Kind == "APIService":
		if service, ok := nestedMap(obj, "spec", "service"); ok {
			m.update(service, "Service")
		}
	case gvk.Group == "apiextensions.k8s.io" && gvk.Kind == "CustomResourceDefinition":
		if service, ok := nestedMap(obj, "spec", "conversion", "webhook", "clientConfig", "service"); ok {
			m.update(service, "Service")
		}
	}
}

// update sets ref["namespace"] if ref points to a moved object of kind.
func (m namespaceMoves) update(ref map[string]any, kind string) {
	name, _ := ref["name"].(string)
	namespace, _ := ref["namespace"].(string)

	if newNamespace, ok := m[movedObject{kind: kind, namespace: namespace, name: name}]; ok {
		ref["namespace"] = newNamespace
	}
}

func nestedMap(obj map[string]any, fields ...string) (map[string]any, bool) {
	current := obj

	for _, field := range fields {
		next, ok := current[field].(map[string]any)
		if !ok {
			return nil, false
		}

		current = next
	}

	return current, true
}
