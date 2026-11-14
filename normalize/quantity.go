package normalize

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// canonicalQuantity returns the canonical string of a quantity, the form the
// api-server stores and returns (1000m -> "1", 0.5 -> "500m",
// 1024Mi -> "1Gi"). ok is false if v is not a quantity.
func canonicalQuantity(v any) (string, bool) {
	var s string

	switch typed := v.(type) {
	case string:
		s = typed
	case int64:
		s = strconv.FormatInt(typed, 10)
	case int:
		s = strconv.Itoa(typed)
	case float64:
		s = strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return "", false
	}

	q, err := resource.ParseQuantity(s)
	if err != nil {
		return "", false
	}

	return q.String(), true
}

func (o *Options) applyQuantityRules(obj map[string]any, t *tracer) {
	if o.Config == nil || len(o.Config.quantityRules) == 0 {
		return
	}

	identity := identityFromObject(obj)
	for _, rule := range o.Config.quantityRules {
		if !rule.matches(identity) {
			continue
		}

		for _, field := range rule.Fields {
			t.step(obj, rule.Origin+": "+field.raw, func() {
				visitFieldPath(obj, field.segments, func(m map[string]any, key string) {
					if canonical, ok := canonicalQuantity(m[key]); ok {
						m[key] = canonical
					}
				})
			})
		}
	}
}

// withCRDQuantities returns o with quantity rules for the fields that CRDs in
// resources declare as quantities, or o itself if there are none. They extend
// quantityFields: a config without quantityFields (like the raw side of
// norm --check) gets none.
func (o *Options) withCRDQuantities(resources []Resource) *Options {
	if o.Config == nil || len(o.Config.quantityRules) == 0 {
		return o
	}

	var rules []ignoreRule

	for _, r := range resources {
		u := r.Unstructured()

		if !isCRD(u) {
			continue
		}

		group, _, _ := unstructured.NestedString(u.Object, "spec", "group")
		kind, _, _ := unstructured.NestedString(u.Object, "spec", "names", "kind")
		versions, _, _ := unstructured.NestedSlice(u.Object, "spec", "versions")

		paths := map[string][]string{}

		for _, v := range versions {
			schema, _, _ := unstructured.NestedMap(asMap(v), "schema", "openAPIV3Schema")
			quantityPaths(schema, nil, paths)
		}

		if len(paths) == 0 {
			continue
		}

		rule := ignoreRule{
			GroupPattern: group, KindPattern: kind, NamespacePattern: "*", NamePattern: "*",
			Origin: "quantity in CRD " + u.GetName(),
		}

		for _, raw := range slices.Sorted(maps.Keys(paths)) {
			rule.Fields = append(rule.Fields, ignoreFieldPath{raw: raw, segments: paths[raw]})
		}

		rules = append(rules, rule)
	}

	if len(rules) == 0 {
		return o
	}

	cfg := Config{}
	if o.Config != nil {
		cfg = *o.Config
	}

	cfg.quantityRules = append(slices.Clone(cfg.quantityRules), rules...)

	withRules := *o
	withRules.Config = &cfg

	return &withRules
}

// quantityPaths collects the paths of quantity fields in a CRD schema:
// int-or-string with the quantity pattern, as generated for
// resource.Quantity by controller-gen. Lists need no segment, they are walked
// implicitly; map values become "*".
func quantityPaths(schema map[string]any, path []string, out map[string][]string) {
	if isQuantitySchema(schema) {
		escaped := make([]string, len(path))
		for i, segment := range path {
			escaped[i] = strings.ReplaceAll(segment, ".", `\.`)
		}

		out[strings.Join(escaped, ".")] = slices.Clone(path)

		return
	}

	properties, _ := schema["properties"].(map[string]any)
	for name, sub := range properties {
		quantityPaths(asMap(sub), append(slices.Clone(path), name), out)
	}

	if items, ok := schema["items"].(map[string]any); ok {
		quantityPaths(items, path, out)
	}

	if additional, ok := schema["additionalProperties"].(map[string]any); ok {
		quantityPaths(additional, append(slices.Clone(path), "*"), out)
	}
}

func isQuantitySchema(schema map[string]any) bool {
	pattern, _ := schema["pattern"].(string)

	return schema["x-kubernetes-int-or-string"] == true && strings.Contains(pattern, "[KMGTPE]i")
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)

	return m
}
