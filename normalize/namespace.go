package normalize

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
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
	// Kubernetes built-ins (as of 1.37, all API groups enabled).
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
	{Group: "resource.k8s.io", Kind: "DeviceTaintRule"}:                               {},
	{Group: "resource.k8s.io", Kind: "ResourcePoolStatusRequest"}:                     {},
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

	// Ecosystem CRDs, scope taken from the upstream CRD manifests (2026-11).

	// Antrea
	{Group: "crd.antrea.io", Kind: "AntreaAgentInfo"}:         {},
	{Group: "crd.antrea.io", Kind: "AntreaControllerInfo"}:    {},
	{Group: "crd.antrea.io", Kind: "AntreaNodeConfig"}:        {},
	{Group: "crd.antrea.io", Kind: "BGPPolicy"}:               {},
	{Group: "crd.antrea.io", Kind: "ClusterGroup"}:            {},
	{Group: "crd.antrea.io", Kind: "ClusterNetworkPolicy"}:    {},
	{Group: "crd.antrea.io", Kind: "Egress"}:                  {},
	{Group: "crd.antrea.io", Kind: "ExternalIPPool"}:          {},
	{Group: "crd.antrea.io", Kind: "FlowExporterDestination"}: {},
	{Group: "crd.antrea.io", Kind: "IPPool"}:                  {},
	{Group: "crd.antrea.io", Kind: "NodeLatencyMonitor"}:      {},
	{Group: "crd.antrea.io", Kind: "PacketCapture"}:           {},
	{Group: "crd.antrea.io", Kind: "SupportBundleCollection"}: {},
	{Group: "crd.antrea.io", Kind: "Tier"}:                    {},
	{Group: "crd.antrea.io", Kind: "Traceflow"}:               {},
	{Group: "crd.antrea.io", Kind: "TrafficControl"}:          {},

	// Argo Rollouts
	{Group: "argoproj.io", Kind: "ClusterAnalysisTemplate"}: {},

	// Argo Workflows
	{Group: "argoproj.io", Kind: "ClusterWorkflowTemplate"}: {},

	// Calico
	{Group: "crd.projectcalico.org", Kind: "BGPConfiguration"}:             {},
	{Group: "crd.projectcalico.org", Kind: "BGPFilter"}:                    {},
	{Group: "crd.projectcalico.org", Kind: "BGPPeer"}:                      {},
	{Group: "crd.projectcalico.org", Kind: "BlockAffinity"}:                {},
	{Group: "crd.projectcalico.org", Kind: "CalicoNodeStatus"}:             {},
	{Group: "crd.projectcalico.org", Kind: "ClusterInformation"}:           {},
	{Group: "crd.projectcalico.org", Kind: "FelixConfiguration"}:           {},
	{Group: "crd.projectcalico.org", Kind: "GlobalNetworkPolicy"}:          {},
	{Group: "crd.projectcalico.org", Kind: "GlobalNetworkSet"}:             {},
	{Group: "crd.projectcalico.org", Kind: "HostEndpoint"}:                 {},
	{Group: "crd.projectcalico.org", Kind: "IPAMBlock"}:                    {},
	{Group: "crd.projectcalico.org", Kind: "IPAMConfig"}:                   {},
	{Group: "crd.projectcalico.org", Kind: "IPAMHandle"}:                   {},
	{Group: "crd.projectcalico.org", Kind: "IPPool"}:                       {},
	{Group: "crd.projectcalico.org", Kind: "IPReservation"}:                {},
	{Group: "crd.projectcalico.org", Kind: "KubeControllersConfiguration"}: {},
	{Group: "crd.projectcalico.org", Kind: "StagedGlobalNetworkPolicy"}:    {},
	{Group: "crd.projectcalico.org", Kind: "Tier"}:                         {},
	{Group: "operator.tigera.io", Kind: "APIServer"}:                       {},
	{Group: "operator.tigera.io", Kind: "GatewayAPI"}:                      {},
	{Group: "operator.tigera.io", Kind: "Goldmane"}:                        {},
	{Group: "operator.tigera.io", Kind: "ImageSet"}:                        {},
	{Group: "operator.tigera.io", Kind: "Installation"}:                    {},
	{Group: "operator.tigera.io", Kind: "Istio"}:                           {},
	{Group: "operator.tigera.io", Kind: "ManagementClusterConnection"}:     {},
	{Group: "operator.tigera.io", Kind: "TigeraStatus"}:                    {},
	{Group: "operator.tigera.io", Kind: "Whisker"}:                         {},

	// cert-manager
	{Group: "cert-manager.io", Kind: "ClusterIssuer"}: {},

	// Cilium
	{Group: "cilium.io", Kind: "CiliumBGPAdvertisement"}:           {},
	{Group: "cilium.io", Kind: "CiliumBGPClusterConfig"}:           {},
	{Group: "cilium.io", Kind: "CiliumBGPNodeConfig"}:              {},
	{Group: "cilium.io", Kind: "CiliumBGPNodeConfigOverride"}:      {},
	{Group: "cilium.io", Kind: "CiliumBGPPeerConfig"}:              {},
	{Group: "cilium.io", Kind: "CiliumCIDRGroup"}:                  {},
	{Group: "cilium.io", Kind: "CiliumClusterwideEnvoyConfig"}:     {},
	{Group: "cilium.io", Kind: "CiliumClusterwideNetworkPolicy"}:   {},
	{Group: "cilium.io", Kind: "CiliumDatapathPlugin"}:             {},
	{Group: "cilium.io", Kind: "CiliumEgressGatewayPolicy"}:        {},
	{Group: "cilium.io", Kind: "CiliumEndpointSlice"}:              {},
	{Group: "cilium.io", Kind: "CiliumIdentity"}:                   {},
	{Group: "cilium.io", Kind: "CiliumL2AnnouncementPolicy"}:       {},
	{Group: "cilium.io", Kind: "CiliumLoadBalancerIPPool"}:         {},
	{Group: "cilium.io", Kind: "CiliumNetworkDriverClusterConfig"}: {},
	{Group: "cilium.io", Kind: "CiliumNetworkDriverNodeConfig"}:    {},
	{Group: "cilium.io", Kind: "CiliumNode"}:                       {},
	{Group: "cilium.io", Kind: "CiliumPodIPPool"}:                  {},
	{Group: "cilium.io", Kind: "CiliumResourceIPPool"}:             {},
	{Group: "cilium.io", Kind: "CiliumResourceNetworkConfig"}:      {},

	// CloudNativePG
	{Group: "postgresql.cnpg.io", Kind: "ClusterImageCatalog"}: {},

	// Cluster API
	{Group: "infrastructure.cluster.x-k8s.io", Kind: "AWSClusterControllerIdentity"}: {},
	{Group: "infrastructure.cluster.x-k8s.io", Kind: "AWSClusterRoleIdentity"}:       {},
	{Group: "infrastructure.cluster.x-k8s.io", Kind: "AWSClusterStaticIdentity"}:     {},
	{Group: "infrastructure.cluster.x-k8s.io", Kind: "ROSAOCMRoleConfig"}:            {},
	{Group: "runtime.cluster.x-k8s.io", Kind: "ExtensionConfig"}:                     {},

	// Crossplane
	{Group: "apiextensions.crossplane.io", Kind: "CompositeResourceDefinition"}:     {},
	{Group: "apiextensions.crossplane.io", Kind: "Composition"}:                     {},
	{Group: "apiextensions.crossplane.io", Kind: "CompositionRevision"}:             {},
	{Group: "apiextensions.crossplane.io", Kind: "EnvironmentConfig"}:               {},
	{Group: "apiextensions.crossplane.io", Kind: "ManagedResourceActivationPolicy"}: {},
	{Group: "apiextensions.crossplane.io", Kind: "ManagedResourceDefinition"}:       {},
	{Group: "apiextensions.crossplane.io", Kind: "Usage"}:                           {},
	{Group: "ops.crossplane.io", Kind: "CronOperation"}:                             {},
	{Group: "ops.crossplane.io", Kind: "Operation"}:                                 {},
	{Group: "ops.crossplane.io", Kind: "WatchOperation"}:                            {},
	{Group: "pkg.crossplane.io", Kind: "Configuration"}:                             {},
	{Group: "pkg.crossplane.io", Kind: "ConfigurationRevision"}:                     {},
	{Group: "pkg.crossplane.io", Kind: "DeploymentRuntimeConfig"}:                   {},
	{Group: "pkg.crossplane.io", Kind: "Function"}:                                  {},
	{Group: "pkg.crossplane.io", Kind: "FunctionRevision"}:                          {},
	{Group: "pkg.crossplane.io", Kind: "ImageConfig"}:                               {},
	{Group: "pkg.crossplane.io", Kind: "Lock"}:                                      {},
	{Group: "pkg.crossplane.io", Kind: "Provider"}:                                  {},
	{Group: "pkg.crossplane.io", Kind: "ProviderRevision"}:                          {},
	{Group: "protection.crossplane.io", Kind: "ClusterUsage"}:                       {},

	// CSI snapshots
	{Group: "groupsnapshot.storage.k8s.io", Kind: "VolumeGroupSnapshotClass"}:   {},
	{Group: "groupsnapshot.storage.k8s.io", Kind: "VolumeGroupSnapshotContent"}: {},
	{Group: "snapshot.storage.k8s.io", Kind: "VolumeSnapshotClass"}:             {},
	{Group: "snapshot.storage.k8s.io", Kind: "VolumeSnapshotContent"}:           {},

	// External Secrets
	{Group: "external-secrets.io", Kind: "ClusterExternalSecret"}:       {},
	{Group: "external-secrets.io", Kind: "ClusterPushSecret"}:           {},
	{Group: "external-secrets.io", Kind: "ClusterSecretStore"}:          {},
	{Group: "generators.external-secrets.io", Kind: "ClusterGenerator"}: {},

	// Gatekeeper
	{Group: "expansion.gatekeeper.sh", Kind: "ExpansionTemplate"}:  {},
	{Group: "externaldata.gatekeeper.sh", Kind: "Provider"}:        {},
	{Group: "mutations.gatekeeper.sh", Kind: "Assign"}:             {},
	{Group: "mutations.gatekeeper.sh", Kind: "AssignImage"}:        {},
	{Group: "mutations.gatekeeper.sh", Kind: "AssignMetadata"}:     {},
	{Group: "mutations.gatekeeper.sh", Kind: "ModifySet"}:          {},
	{Group: "syncset.gatekeeper.sh", Kind: "SyncSet"}:              {},
	{Group: "templates.gatekeeper.sh", Kind: "ConstraintTemplate"}: {},

	// Gateway API
	{Group: "gateway.networking.k8s.io", Kind: "GatewayClass"}: {},
	{Group: "gateway.networking.x-k8s.io", Kind: "XMesh"}:      {},

	// k3s
	{Group: "k3s.cattle.io", Kind: "ETCDSnapshotFile"}: {},

	// Karpenter
	{Group: "karpenter.k8s.aws", Kind: "EC2NodeClass"}: {},
	{Group: "karpenter.sh", Kind: "NodeClaim"}:         {},
	{Group: "karpenter.sh", Kind: "NodePool"}:          {},

	// KEDA
	{Group: "eventing.keda.sh", Kind: "ClusterCloudEventSource"}: {},
	{Group: "keda.sh", Kind: "ClusterTriggerAuthentication"}:     {},

	// Knative
	{Group: "networking.internal.knative.dev", Kind: "ClusterDomainClaim"}: {},

	// Kong
	{Group: "configuration.konghq.com", Kind: "KongClusterPlugin"}: {},
	{Group: "configuration.konghq.com", Kind: "KongLicense"}:       {},
	{Group: "configuration.konghq.com", Kind: "KongVault"}:         {},

	// kro
	{Group: "internal.kro.run", Kind: "GraphRevision"}:  {},
	{Group: "kro.run", Kind: "ResourceGraphDefinition"}: {},

	// KubeVirt CDI
	{Group: "cdi.kubevirt.io", Kind: "CDI"}: {},

	// Kubewarden
	{Group: "policies.kubewarden.io", Kind: "PolicyServer"}: {},

	// Kueue
	{Group: "kueue.x-k8s.io", Kind: "AdmissionCheck"}:            {},
	{Group: "kueue.x-k8s.io", Kind: "ClusterQueue"}:              {},
	{Group: "kueue.x-k8s.io", Kind: "Cohort"}:                    {},
	{Group: "kueue.x-k8s.io", Kind: "MultiKueueCluster"}:         {},
	{Group: "kueue.x-k8s.io", Kind: "MultiKueueConfig"}:          {},
	{Group: "kueue.x-k8s.io", Kind: "ProvisioningRequestConfig"}: {},
	{Group: "kueue.x-k8s.io", Kind: "ResourceFlavor"}:            {},
	{Group: "kueue.x-k8s.io", Kind: "Topology"}:                  {},
	{Group: "kueue.x-k8s.io", Kind: "WorkloadPriorityClass"}:     {},

	// KWOK
	{Group: "kwok.x-k8s.io", Kind: "ClusterAttach"}:        {},
	{Group: "kwok.x-k8s.io", Kind: "ClusterExec"}:          {},
	{Group: "kwok.x-k8s.io", Kind: "ClusterLogs"}:          {},
	{Group: "kwok.x-k8s.io", Kind: "ClusterPortForward"}:   {},
	{Group: "kwok.x-k8s.io", Kind: "ClusterResourceUsage"}: {},
	{Group: "kwok.x-k8s.io", Kind: "Metric"}:               {},
	{Group: "kwok.x-k8s.io", Kind: "Stage"}:                {},

	// Kyverno
	{Group: "kyverno.io", Kind: "ClusterCleanupPolicy"}:           {},
	{Group: "kyverno.io", Kind: "ClusterPolicy"}:                  {},
	{Group: "kyverno.io", Kind: "GlobalContextEntry"}:             {},
	{Group: "policies.kyverno.io", Kind: "DeletingPolicy"}:        {},
	{Group: "policies.kyverno.io", Kind: "GeneratingPolicy"}:      {},
	{Group: "policies.kyverno.io", Kind: "ImageValidatingPolicy"}: {},
	{Group: "policies.kyverno.io", Kind: "MutatingPolicy"}:        {},
	{Group: "policies.kyverno.io", Kind: "ValidatingPolicy"}:      {},
	{Group: "reports.kyverno.io", Kind: "ClusterEphemeralReport"}: {},
	{Group: "wgpolicyk8s.io", Kind: "ClusterPolicyReport"}:        {},

	// Liqo
	{Group: "core.liqo.io", Kind: "ForeignCluster"}:     {},
	{Group: "networking.liqo.io", Kind: "InternalNode"}: {},

	// Network Policy API
	{Group: "policy.networking.k8s.io", Kind: "AdminNetworkPolicy"}:         {},
	{Group: "policy.networking.k8s.io", Kind: "BaselineAdminNetworkPolicy"}: {},
	{Group: "policy.networking.k8s.io", Kind: "ClusterNetworkPolicy"}:       {},

	// Node Feature Discovery
	{Group: "nfd.k8s-sigs.io", Kind: "NodeFeatureRule"}: {},

	// NVIDIA GPU Operator
	{Group: "nvidia.com", Kind: "ClusterPolicy"}: {},
	{Group: "nvidia.com", Kind: "GPUCluster"}:    {},
	{Group: "nvidia.com", Kind: "NVIDIADriver"}:  {},

	// OpenShift
	{Group: "security.openshift.io", Kind: "SecurityContextConstraints"}: {},

	// Operator Lifecycle Manager
	{Group: "operators.coreos.com", Kind: "OLMConfig"}: {},
	{Group: "operators.coreos.com", Kind: "Operator"}:  {},

	// Rook
	{Group: "objectbucket.io", Kind: "ObjectBucket"}: {},

	// Tekton
	{Group: "triggers.tekton.dev", Kind: "ClusterInterceptor"}:    {},
	{Group: "triggers.tekton.dev", Kind: "ClusterTriggerBinding"}: {},

	// Traefik Hub
	{Group: "hub.traefik.io", Kind: "AccessControlPolicy"}: {},

	// trust-manager
	{Group: "trust.cert-manager.io", Kind: "Bundle"}: {},

	// Volcano
	{Group: "nodeinfo.volcano.sh", Kind: "Numatopology"}: {},
	{Group: "scheduling.volcano.sh", Kind: "Queue"}:      {},
	{Group: "shard.volcano.sh", Kind: "NodeShard"}:       {},
	{Group: "topology.volcano.sh", Kind: "HyperNode"}:    {},
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

		if !isCRD(u) {
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

func isCRD(u *unstructured.Unstructured) bool {
	gvk := u.GroupVersionKind()

	return gvk.Group == "apiextensions.k8s.io" && gvk.Kind == "CustomResourceDefinition"
}

// isBuiltinClusterScoped reports whether the built-in list knows gk as
// cluster-scoped.
func isBuiltinClusterScoped(gk schema.GroupKind) bool {
	// Gatekeeper creates one cluster-scoped CRD per ConstraintTemplate, the
	// kind is chosen by the user.
	if gk.Group == "constraints.gatekeeper.sh" {
		return true
	}

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

	return isBuiltinClusterScoped(gk)
}

var dns1123LabelRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// validateNamespaceName checks that ns is a valid RFC 1123 label.
func validateNamespaceName(ns string) error {
	if len(ns) > 63 || !dns1123LabelRegex.MatchString(ns) {
		return fmt.Errorf("invalid namespace %q: must be a lowercase RFC 1123 label (a-z, 0-9, '-', max 63 chars)", ns)
	}

	return nil
}

// validate checks option combinations.
func (o *Options) validate() error {
	if o.AddNamespaceIfMissing != "" && o.OverwriteNamespace != "" {
		return errors.New("add-namespace-if-missing and overwrite-namespace are mutually exclusive")
	}

	for _, ns := range []string{o.AddNamespaceIfMissing, o.OverwriteNamespace} {
		if ns == "" {
			continue
		}

		err := validateNamespaceName(ns)
		if err != nil {
			return err
		}
	}

	if o.SecretsAsStringData && o.RedactSecrets {
		return errors.New("secrets-as-stringdata and redact-secrets are mutually exclusive")
	}

	if o.RenderedBy != "" {
		if strings.ContainsFunc(o.RenderedBy, unicode.IsControl) {
			return fmt.Errorf("rendered-by %q contains control characters", o.RenderedBy)
		}

		if _, ok := o.SetAnnotations[RenderedByAnnotation]; ok {
			return fmt.Errorf("rendered-by and a set annotation %s are mutually exclusive", RenderedByAnnotation)
		}
	}

	return validateMetadataValues(o.SetLabels, o.SetAnnotations)
}

// validateMetadataValues checks the keys and label values of SetLabels and
// SetAnnotations like the api-server does (it lowercases annotation keys
// before checking them; it also limits the total size of annotations,
// which is not checked here).
func validateMetadataValues(labels, annotations map[string]string) error {
	for _, set := range []struct {
		what   string
		values map[string]string
	}{{kindLabel, labels}, {kindAnnotation, annotations}} {
		what := set.what
		for _, k := range slices.Sorted(maps.Keys(set.values)) {
			v := set.values[k]

			key := k
			if what == kindAnnotation {
				key = strings.ToLower(k)
			}

			if errs := validation.IsQualifiedName(key); len(errs) > 0 {
				return fmt.Errorf("invalid %s key %q: %s", what, k, strings.Join(errs, "; "))
			}

			if what != kindLabel {
				continue
			}

			if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
				return fmt.Errorf("invalid value of label %q: %s", k, strings.Join(errs, "; "))
			}
		}
	}

	return nil
}

// namespaceMove describes how --add-namespace-if-missing or
// --overwrite-namespace moves namespaces.
type namespaceMove struct {
	target string
	reason string
	// moved holds the namespaces that move to target.
	moved map[string]bool
	// inInput holds the namespaces of the namespaced resources in the input,
	// and target.
	inInput map[string]bool
}

// namespaceMove decides which namespaces move: with OverwriteNamespace every
// namespace of a namespaced resource in the input (and ""), with
// AddNamespaceIfMissing only "". It returns nil if no namespace option is set.
func (o *Options) namespaceMove(resources []Resource, scope *scopeResolver) *namespaceMove {
	m := &namespaceMove{moved: map[string]bool{"": true}, inInput: map[string]bool{}}

	switch {
	case o.OverwriteNamespace != "":
		m.target, m.reason = o.OverwriteNamespace, "--overwrite-namespace"
	case o.AddNamespaceIfMissing != "":
		m.target, m.reason = o.AddNamespaceIfMissing, "--add-namespace-if-missing"
	default:
		return nil
	}

	for _, r := range resources {
		u := r.Unstructured()
		if !r.SOPSEncrypted && !scope.isClusterScoped(u.GroupVersionKind().GroupKind()) {
			m.inInput[u.GetNamespace()] = true
		}
	}

	if o.OverwriteNamespace != "" {
		maps.Copy(m.moved, m.inInput)
	}

	// After the move the target namespace is in use.
	m.inInput[m.target] = true

	return m
}

// to returns the new namespace for something in namespace old, or "" if it
// stays where it is.
func (m *namespaceMove) to(old string) string {
	if m.moved[old] {
		return m.target
	}

	return ""
}

// namespaceRefs lists references to namespaces in other objects. path leads
// to a required field of the map that holds the "namespace" field.
var namespaceRefs = []struct {
	gk   schema.GroupKind
	path []string
}{
	{schema.GroupKind{Group: "rbac.authorization.k8s.io", Kind: "RoleBinding"}, []string{"subjects", "kind"}},
	{schema.GroupKind{Group: "rbac.authorization.k8s.io", Kind: "ClusterRoleBinding"}, []string{"subjects", "kind"}},
	{schema.GroupKind{Group: "admissionregistration.k8s.io", Kind: "MutatingWebhookConfiguration"}, []string{"webhooks", "clientConfig", "service", "name"}},
	{schema.GroupKind{Group: "admissionregistration.k8s.io", Kind: "ValidatingWebhookConfiguration"}, []string{"webhooks", "clientConfig", "service", "name"}},
	{schema.GroupKind{Group: "apiregistration.k8s.io", Kind: "APIService"}, []string{"spec", "service", "name"}},
	{schema.GroupKind{Group: "apiextensions.k8s.io", Kind: "CustomResourceDefinition"}, []string{"spec", "conversion", "webhook", "clientConfig", "service", "name"}},
}

// applyNamespace sets metadata.namespace according to AddNamespaceIfMissing
// or OverwriteNamespace and moves references to namespaces that move: RBAC
// ServiceAccount subjects, service references of webhooks, APIServices and
// CRD conversion webhooks, and the cert-manager inject-ca-from annotations. Cluster-scoped resources keep their namespace; a
// cluster-scoped resource that carries a namespace is left as is.
func (o *Options) applyNamespace(u *unstructured.Unstructured, scope *scopeResolver, move *namespaceMove, t *tracer) {
	if move == nil {
		return
	}

	reason := move.reason
	gk := u.GroupVersionKind().GroupKind()

	if gk == (schema.GroupKind{Kind: "Namespace"}) && move.moved[u.GetName()] && u.GetName() != move.target {
		o.warnf("Namespace %s is in the input, but %s moves its resources to %s", u.GetName(), reason, move.target)
	}

	if !scope.isClusterScoped(gk) {
		if ns := move.to(u.GetNamespace()); ns != "" && ns != u.GetNamespace() {
			t.step(u.Object, reason, func() { u.SetNamespace(ns) })
		}
	}

	o.moveReferences(u, gk, move, t)
}

// moveReferences moves the namespaces that other objects refer to (see
// applyNamespace).
func (o *Options) moveReferences(u *unstructured.Unstructured, gk schema.GroupKind, move *namespaceMove, t *tracer) {
	reason := move.reason
	warned := false

	// moveRef moves a reference to namespace old with set.
	moveRef := func(old string, set func(ns string)) {
		switch ns := move.to(old); {
		case ns != "" && ns != old:
			t.step(u.Object, reason, func() { set(ns) })
		case old == "default" && !move.inInput["default"] && !warned:
			warned = true

			o.warnf("%s %s refers to namespace default, which no resource of the input uses. Rendered without helm template --namespace?",
				gk.Kind, u.GetName())
		}
	}

	for _, key := range []string{"cert-manager.io/inject-ca-from", "cert-manager.io/inject-ca-from-secret"} {
		annotations := u.GetAnnotations()

		old, name, ok := strings.Cut(annotations[key], "/") // value: namespace/name
		if ok {
			moveRef(old, func(ns string) {
				annotations[key] = ns + "/" + name
				u.SetAnnotations(annotations)
			})
		}
	}

	for _, ref := range namespaceRefs {
		if ref.gk != gk {
			continue
		}

		visitFieldPath(u.Object, ref.path, func(m map[string]any, key string) {
			if key == "kind" && m["kind"] != "ServiceAccount" {
				return // User and Group subjects have no namespace
			}

			old, _ := m["namespace"].(string)
			if old == "" && gk.Kind == "RoleBinding" {
				return // means the namespace of the RoleBinding
			}

			moveRef(old, func(ns string) { m["namespace"] = ns })
		})
	}
}
