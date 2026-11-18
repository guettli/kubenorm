package normalize

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// crdVersion identifies the schema of one version of a CRD.
type crdVersion struct{ group, kind, version string }

// withCRDRules returns o with the rules derived from the CRDs in resources,
// or o itself if there are none:
//   - quantity rules for the fields of type resource.Quantity,
//   - remove rules for the fields that hold the default of the schema: the
//     api-server sets them when they are absent, so both forms are the same.
//
// The defaults may differ per version, so there is one rule per version. If
// a CRD is known twice (--crds and the input), the later one wins: a union
// of both default sets would hide real differences.
func (o *Options) withCRDRules(resources []Resource) *Options {
	var quantities, defaults []removeRule

	replaced := map[crdVersion]bool{}

	for _, r := range resources {
		u := r.Unstructured()
		if !isCRD(u) {
			continue
		}

		quantity, versions := crdRules(u)
		quantities = append(quantities, quantity...)

		for _, v := range versions {
			key := crdVersion{v.GroupPattern, v.KindPattern, v.Version}
			replaced[key] = true
			defaults = slices.DeleteFunc(defaults, func(d removeRule) bool {
				return crdVersion{d.GroupPattern, d.KindPattern, d.Version} == key
			})

			if len(v.Fields) > 0 {
				defaults = append(defaults, v)
			}
		}
	}

	if len(quantities) == 0 && len(replaced) == 0 {
		return o
	}

	cfg := Config{}
	if o.Config != nil {
		cfg = *o.Config
	}

	cfg.quantityRules = append(slices.Clone(cfg.quantityRules), quantities...)
	cfg.removeRules = slices.DeleteFunc(slices.Clone(cfg.removeRules), func(r removeRule) bool {
		return r.Version != "" && replaced[crdVersion{r.GroupPattern, r.KindPattern, r.Version}]
	})
	cfg.removeRules = append(cfg.removeRules, defaults...)

	with := *o
	with.Config = &cfg

	return &with
}

// crdRules returns the quantity rule of a CRD (if it has quantity fields)
// and one default rule per version (Fields empty if it has no defaults).
func crdRules(crd *unstructured.Unstructured) (quantity, defaults []removeRule) {
	group, _, _ := unstructured.NestedString(crd.Object, "spec", "group")
	kind, _, _ := unstructured.NestedString(crd.Object, "spec", "names", "kind")
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	rule := func(origin, version string, fields map[string]removeFieldPath) removeRule {
		return removeRule{
			GroupPattern: group, KindPattern: kind, NamespacePattern: "*", NamePattern: "*",
			Origin: origin + " in CRD " + crd.GetName(), Version: version, Fields: deepestFirst(fields),
		}
	}

	quantityFields := map[string]removeFieldPath{}

	for _, v := range versions {
		version, _, _ := unstructured.NestedString(asMap(v), "name")
		schema, _, _ := unstructured.NestedMap(asMap(v), "schema", "openAPIV3Schema")
		defaultFields := map[string]removeFieldPath{}

		walkSchema(schema, nil, false, func(s map[string]any, path []string, isProperty bool) {
			raw := escapePath(path)
			if isQuantitySchema(s) {
				quantityFields[raw] = removeFieldPath{raw: raw, segments: path}
			}

			if def, ok := s["default"]; ok && def != nil && isProperty {
				defaultFields[raw] = removeFieldPath{
					raw: raw, segments: path, value: prunedDefault(deepCopy(def), s), valueConstrained: true,
				}
			}
		})

		defaults = append(defaults, rule("default ("+version+")", version, defaultFields))
	}

	if len(quantityFields) > 0 {
		quantity = append(quantity, rule("quantity", "", quantityFields))
	}

	return quantity, defaults
}

// walkSchema calls fn for the schema at path and every schema below it.
// Lists need no segment, they are walked implicitly; map values become "*".
// isProperty is false for list items and map values: their default never
// applies, the item exists.
func walkSchema(schema map[string]any, path []string, isProperty bool, fn func(schema map[string]any, path []string, isProperty bool)) {
	fn(schema, path, isProperty)

	properties, _ := schema["properties"].(map[string]any)
	for name, sub := range properties {
		walkSchema(asMap(sub), append(slices.Clone(path), name), true, fn)
	}

	if items, ok := schema["items"].(map[string]any); ok {
		walkSchema(items, path, false, fn)
	}

	if additional, ok := schema["additionalProperties"].(map[string]any); ok {
		walkSchema(additional, append(slices.Clone(path), "*"), false, fn)
	}
}

func escapePath(path []string) string {
	escaped := make([]string, len(path))
	for i, segment := range path {
		escaped[i] = strings.ReplaceAll(segment, ".", `\.`)
	}

	return strings.Join(escaped, ".")
}

// deepestFirst returns the fields sorted by depth, deepest first: a parent
// is compared after its children lost their defaults.
func deepestFirst(fields map[string]removeFieldPath) []removeFieldPath {
	sorted := slices.Collect(maps.Values(fields))
	slices.SortFunc(sorted, func(a, b removeFieldPath) int {
		return cmp.Or(cmp.Compare(len(b.segments), len(a.segments)), cmp.Compare(a.raw, b.raw))
	})

	return sorted
}

// prunedDefault returns v (a default) as a field holds it when the rule of
// schema compares it: quantities are canonical (the quantity rules run
// first), and the child rules removed the fields that hold their own
// default. So default: {a: 1} with a default of 1 for a becomes {}.
func prunedDefault(v any, schema map[string]any) any {
	if canonical, ok := canonicalQuantity(v); ok && isQuantitySchema(schema) {
		return canonical
	}

	switch typed := v.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		additional, _ := schema["additionalProperties"].(map[string]any)

		for key, val := range typed {
			// A property default applies when the key is absent, a default
			// of additionalProperties never does.
			sub, isProperty := asMap(properties[key]), true
			if sub == nil {
				sub, isProperty = additional, false
			}

			if sub == nil {
				continue
			}

			val = prunedDefault(val, sub)
			typed[key] = val

			if def, ok := sub["default"]; ok && def != nil && isProperty && valueMatches(val, prunedDefault(deepCopy(def), sub)) {
				delete(typed, key)
			}
		}
	case []any:
		if items := asMap(schema["items"]); items != nil {
			for i := range typed {
				typed[i] = prunedDefault(typed[i], items)
			}
		}
	}

	return v
}
