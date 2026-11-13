// Package normalize normalizes Kubernetes resources so that two YAML
// representations of the same desired state become byte-identical.
//
// It removes server-managed and defaulted fields according to a Config
// (see CommonConfig for the embedded defaults), drops resources matching
// skip rules and writes them in a stable layout:
//
//	NAMESPACE/[GROUP_]KIND/NAME.yaml
//
// Cluster-scoped resources use the namespace directory "_cluster".
package normalize

import (
	"fmt"
	"io"
	"path/filepath"
	"regexp"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	// ClusterNamespace is the pseudo namespace used for cluster-scoped
	// resources, both for rule matching and as output directory.
	ClusterNamespace = "_cluster"

	// RedactionMarker replaces Secret values when Options.RedactSecrets is set.
	RedactionMarker = "REDCATED-BY-DUMPALL"

	fieldMetadata         = "metadata"
	kindSecret            = "Secret"
	lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"
)

// Options controls filtering and normalization.
type Options struct {
	// Config holds removeFields, skipResources and excludeNamespaces rules.
	// nil applies no rules.
	Config *Config

	// Namespaces restricts output to these namespaces (empty = all).
	// Cluster-scoped resources are dropped, except Namespace objects whose
	// name is in the set.
	Namespaces map[string]struct{}

	// Kinds restricts output to kinds matching one of these globs (empty = all).
	Kinds []string

	// NameRegex restricts output to resources whose name matches (nil = all).
	NameRegex *regexp.Regexp

	// SkipOwned drops resources with a controlling owner reference and
	// resources Kubernetes autogenerates (aggregated ClusterRoles, RBAC
	// bootstrap objects, default ServiceAccount, kube-root-ca.crt ConfigMap).
	SkipOwned bool

	// KeepManagedFields keeps metadata.managedFields, even if a rule removes it.
	KeepManagedFields bool

	// RedactSecrets replaces values of core/v1 Secrets with RedactionMarker
	// and drops the last-applied-configuration annotation of Secrets.
	RedactSecrets bool

	// SecretsAsStringData converts base64 Secret data into stringData.
	SecretsAsStringData bool

	// AddNamespaceIfMissing sets metadata.namespace on namespaced resources
	// that have none. Cluster-scoped resources are left alone (see
	// isBuiltinClusterScoped). Applied before filtering.
	AddNamespaceIfMissing string

	// OverwriteNamespace sets metadata.namespace on all namespaced resources,
	// replacing an existing value. Applied before filtering.
	OverwriteNamespace string
}

// objectIdentity identifies a resource for rule matching. Cluster-scoped resources
// have Namespace == ClusterNamespace.
type objectIdentity struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
}

// identityOf returns the identity of item.
func identityOf(item *unstructured.Unstructured, isNamespaced bool) objectIdentity {
	namespace := item.GetNamespace()
	if !isNamespaced {
		namespace = ClusterNamespace
	}

	return objectIdentity{
		Group:     getGroup(item.GetAPIVersion()),
		Kind:      item.GetKind(),
		Namespace: namespace,
		Name:      item.GetName(),
	}
}

// Normalize modifies obj in place: removes managed fields, handles Secrets,
// applies the removeFields rules and prunes empty labels/annotations.
func (o *Options) Normalize(obj map[string]any) error {
	return o.normalize(obj, nil)
}

// normalize is Normalize; t records every step for Explain (nil: no tracing).
func (o *Options) normalize(obj map[string]any, t *tracer) error {
	metadata, ok := obj[fieldMetadata].(map[string]any)
	if !ok {
		return fmt.Errorf("metadata not found in object")
	}

	if !o.KeepManagedFields {
		t.step(obj, "managedFields are removed (--keep-managed-fields keeps them)", func() {
			delete(metadata, "managedFields")
		})
	}

	if o.RedactSecrets {
		t.step(obj, "--redact-secrets", func() { redactSecretValues(obj) })
	}

	if o.SecretsAsStringData {
		t.step(obj, "--secrets-as-stringdata", func() { convertSecretDataToStringData(obj) })
	}

	o.applyQuantityRules(obj, t)
	o.applyRemoveRules(obj, t)
	t.step(obj, "empty labels/annotations are removed", func() { pruneEmptyMetadataMaps(obj) })

	return nil
}

func (o *Options) applyRemoveRules(obj map[string]any, t *tracer) {
	if !o.Config.HasRemoveRules() {
		return
	}

	identity := identityFromObject(obj)
	for _, rule := range o.Config.removeRules {
		if !rule.matches(identity) {
			continue
		}

		for _, field := range rule.Fields {
			if o.KeepManagedFields && isManagedFieldsPath(field.segments) {
				continue
			}

			t.step(obj, rule.Origin+": "+field.raw, func() {
				deleteField(obj, field)
			})
		}
	}
}

func identityFromObject(obj map[string]any) objectIdentity {
	u := &unstructured.Unstructured{Object: obj}

	return identityOf(u, u.GetNamespace() != "")
}

func pruneEmptyMetadataMaps(obj map[string]any) {
	metadata, ok := obj[fieldMetadata].(map[string]any)
	if !ok {
		return
	}

	pruneEmptyMetadataMap(metadata, "annotations")
	pruneEmptyMetadataMap(metadata, "labels")
}

func pruneEmptyMetadataMap(metadata map[string]any, field string) {
	if typed, ok := metadata[field].(map[string]any); ok && len(typed) == 0 {
		delete(metadata, field)
	}
}

// RelPath returns the stable relative output path of a resource:
// NAMESPACE/KIND/NAME.yaml for the core group, NAMESPACE/GROUP_KIND/NAME.yaml
// otherwise. Cluster-scoped resources use ClusterNamespace.
func RelPath(item *unstructured.Unstructured, isNamespaced bool) string {
	ns := item.GetNamespace()
	if !isNamespaced {
		ns = ClusterNamespace
	}

	gvk := item.GroupVersionKind()

	kindDir := gvk.Kind
	if gvk.Group != "" {
		kindDir = gvk.Group + "_" + gvk.Kind
	}

	return filepath.Join(ns, kindDir, sanitizePath(item.GetName())+".yaml")
}

var sanitizePathRegex = regexp.MustCompile(`[\\/:*?"'<>|!@#$%^&()+={}\[\];,]`)

func sanitizePath(path string) string {
	return sanitizePathRegex.ReplaceAllString(path, "_")
}

// Encode writes obj as YAML exactly like `kubectl get -o yaml`
// (sigs.k8s.io/yaml: keys sorted, 2-space indent, list items not indented),
// but with a deterministic key order, see marshalYAML.
// Each header line is written first as a "# " comment.
func Encode(w io.Writer, obj map[string]any, headerLines ...string) error {
	for _, line := range headerLines {
		_, err := fmt.Fprintf(w, "# %s\n", line)
		if err != nil {
			return fmt.Errorf("writing header: %w", err)
		}
	}

	data, err := marshalYAML(obj)
	if err != nil {
		return fmt.Errorf("encoding YAML: %w", err)
	}

	_, err = w.Write(data)
	if err != nil {
		return fmt.Errorf("writing YAML: %w", err)
	}

	return nil
}
