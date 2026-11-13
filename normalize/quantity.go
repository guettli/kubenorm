package normalize

import (
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"
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
