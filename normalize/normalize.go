package normalize

import (
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	// ClusterNamespace is the pseudo namespace used for cluster-scoped
	// resources, both for rule matching and as output directory.
	ClusterNamespace = "_cluster"

	// RedactionMarker replaces Secret values when Options.RedactSecrets is set.
	RedactionMarker = "REDACTED"

	// RenderedByAnnotation names the script or pipeline that rendered a
	// resource, see Options.RenderedBy and the --rendered-by flag of the
	// kubenorm CLI. Unlike
	// app.kubernetes.io/managed-by (the tool that manages the resource in
	// the cluster) it allows any value, for example a path.
	RenderedByAnnotation = "kubenorm.guettli.github.io/rendered-by"

	fieldMetadata         = "metadata"
	kindSecret            = "Secret"
	lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"
)

// Options controls filtering and normalization.
type Options struct {
	// Config holds the rules (removeFields, skipResources, excludeNamespaces,
	// quantityFields, ...), see LoadConfig. nil applies no rules. Quantity
	// fields declared by CRDs in the input (and WithCRDs) are canonicalized in
	// any case.
	Config *Config

	// Namespaces restricts output to these namespaces (empty = all).
	// Cluster-scoped resources are dropped, except Namespace objects whose
	// name is in the set.
	Namespaces []string

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
	// that have none. Cluster-scoped resources (built-in kinds, CRDs in the
	// input with scope Cluster, clusterScopedResources of the config) are
	// left alone. Applied before filtering.
	AddNamespaceIfMissing string

	// OverwriteNamespace sets metadata.namespace on all namespaced resources,
	// replacing an existing value. Applied before filtering.
	OverwriteNamespace string

	// SetLabels sets these labels in metadata.labels of every resource,
	// replacing existing values. Applied after the remove rules, so a rule
	// does not remove them. Pod templates and selectors are not changed.
	SetLabels map[string]string

	// SetAnnotations sets these annotations in metadata.annotations of every
	// resource, like SetLabels.
	SetAnnotations map[string]string

	// RenderedBy sets the annotation RenderedByAnnotation of every resource,
	// like SetAnnotations. It must not contain control characters.
	RenderedBy string

	// Warn receives warnings about the input, for example a reference to a
	// namespace that the namespace options cannot move. Nil discards them.
	Warn func(msg string)

	// crds are known in addition to the CRDs in the input, see WithCRDs.
	crds []Resource
}

// WithCRDs returns a copy of o that also knows these
// CustomResourceDefinitions, for example the CRDs of a cluster. Other
// resources are ignored. Like CRDs in the input of Process, their quantity
// fields are canonicalized (also by Normalize) and their scope is used by the
// namespace options.
func (o *Options) WithCRDs(resources []Resource) *Options {
	with := *o.withCRDQuantities(resources)
	with.crds = slices.Clone(o.crds)

	for _, r := range resources {
		if isCRD(r.Unstructured()) {
			with.crds = append(with.crds, r)
		}
	}

	return &with
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
// canonicalizes quantityFields, applies the removeFields and setMetadata
// rules, sets SetLabels, SetAnnotations and RenderedBy and prunes empty labels/annotations.
//
// Unlike Process, it handles a single object and skips everything that needs
// the other resources or decides about keeping one: no namespace options
// (AddNamespaceIfMissing, OverwriteNamespace), no filters (skip rules,
// excludeNamespaces, Namespaces, Kinds, NameRegex, SkipOwned; see Include)
// and no detection of quantity fields from CRDs in the input (use WithCRDs).
// Use Process for files and streams of resources; use Include and Normalize
// for objects streamed one by one, for example from an api-server.
func (o *Options) Normalize(obj map[string]any) error {
	err := o.validate()
	if err != nil {
		return err
	}

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
	o.applySetMetadataRules(obj, metadata, t)
	setMetadataMap(obj, metadata, "labels", "--set-label", o.SetLabels, t)
	setMetadataMap(obj, metadata, "annotations", "--set-annotation", o.SetAnnotations, t)

	if o.RenderedBy != "" {
		setMetadataMap(obj, metadata, "annotations", "--rendered-by", map[string]string{RenderedByAnnotation: o.RenderedBy}, t)
	}

	t.step(obj, "empty labels/annotations are removed", func() { pruneEmptyMetadataMaps(obj) })

	return nil
}

func (o *Options) applyRemoveRules(obj map[string]any, t *tracer) {
	if o.Config == nil || len(o.Config.removeRules) == 0 {
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

// setMetadataMap sets the values in metadata.<field> (labels or
// annotations), creating the map if needed.
func setMetadataMap(obj, metadata map[string]any, field, reason string, values map[string]string, t *tracer) {
	for _, k := range slices.Sorted(maps.Keys(values)) {
		t.step(obj, reason+" "+k+"="+values[k], func() {
			m, ok := metadata[field].(map[string]any)
			if !ok {
				m = map[string]any{}
				metadata[field] = m
			}

			m[k] = values[k]
		})
	}
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

	return filepath.Join(sanitizePath(ns), sanitizePath(kindDir), sanitizePath(item.GetName())+".yaml")
}

var sanitizePathRegex = regexp.MustCompile(`[\\/:*?"'<>|!@#$%^&()+={}\[\];,]`)

// sanitizePath makes one path element of untrusted input safe: no
// separators, and never "." or ".." (a namespace ".." would leave the
// output directory).
func sanitizePath(path string) string {
	if path == "." || path == ".." {
		return strings.Repeat("_", len(path))
	}

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

func (o *Options) warnf(format string, args ...any) {
	if o.Warn != nil {
		o.Warn(fmt.Sprintf(format, args...))
	}
}
