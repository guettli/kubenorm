package normalize

import (
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"
)

// CanonicalQuantity returns the canonical string of a quantity, the form the
// api-server stores and returns (1000m -> "1", 0.5 -> "500m",
// 1024Mi -> "1Gi"). ok is false if v is not a quantity.
func CanonicalQuantity(v any) (string, bool) {
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

func (o *Options) applyQuantityRules(obj map[string]any) {
	if o.Config == nil || len(o.Config.quantityRules) == 0 {
		return
	}

	identity := identityFromObject(obj)
	for _, rule := range o.Config.quantityRules {
		if !rule.matches(identity) {
			continue
		}

		for _, field := range rule.Fields {
			visitFieldPath(obj, field.segments, func(m map[string]any, key string) {
				if canonical, ok := CanonicalQuantity(m[key]); ok {
					m[key] = canonical
				}
			})
		}
	}
}

// visitFieldPath calls fn for every map entry matching the path segments
// (same syntax as removeFields: globs, "..." and implicit list walking).
func visitFieldPath(current any, segments []string, fn func(m map[string]any, key string)) {
	if len(segments) == 0 {
		return
	}

	if segments[0] == deepWildcard {
		visitFieldPathRecursive(current, segments[1:], fn)
		return
	}

	switch typed := current.(type) {
	case map[string]any:
		for key, next := range typed {
			// A plain "*" matches every key, including extended resource
			// names with a slash such as nvidia.com/gpu.
			if segments[0] != "*" && !matchGlob(segments[0], key) {
				continue
			}

			if len(segments) == 1 {
				fn(typed, key)
				continue
			}

			visitFieldPath(next, segments[1:], fn)
		}
	case []any:
		for _, entry := range typed {
			visitFieldPath(entry, segments, fn)
		}
	}
}

func visitFieldPathRecursive(current any, remaining []string, fn func(m map[string]any, key string)) {
	if len(remaining) == 0 {
		return
	}

	visitFieldPath(current, remaining, fn)

	switch typed := current.(type) {
	case map[string]any:
		for _, value := range typed {
			visitFieldPathRecursive(value, remaining, fn)
		}
	case []any:
		for _, entry := range typed {
			visitFieldPathRecursive(entry, remaining, fn)
		}
	}
}
