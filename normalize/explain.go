package normalize

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Change is one modification made while normalizing a resource.
type Change struct {
	// Path is the field, e.g. spec.template.spec.containers[0].image.
	Path string
	// Old and New are nil if the field was added or removed.
	Old, New any
	// Reason names the rule or option that made the change.
	Reason string
}

// Explanation tells what normalization does with one resource.
type Explanation struct {
	Source string
	// Path is the output path, see RelPath.
	Path string
	// Dropped is the reason why the resource is not part of the output, or
	// "" if it is kept.
	Dropped string
	Changes []Change
}

// Explain processes the resources like Process and reports, per resource,
// every change and the rule that made it.
func (o *Options) Explain(resources []Resource) ([]Explanation, error) {
	_, explanations, err := o.process(resources, true)

	return explanations, err
}

// tracer records changes made by steps. A nil tracer only runs the steps.
type tracer struct {
	changes []Change
}

func (t *tracer) step(obj map[string]any, reason string, fn func()) {
	if t == nil {
		fn()
		return
	}

	before := deepCopy(obj)

	fn()

	var changes []Change

	diffValues(before, obj, "", reason, &changes)
	t.changes = append(t.changes, changes...)
}

func deepCopy(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, val := range typed {
			out[k] = deepCopy(val)
		}

		return out
	case []any:
		out := make([]any, len(typed))
		for i, val := range typed {
			out[i] = deepCopy(val)
		}

		return out
	default:
		return v
	}
}

func diffValues(before, after any, path, reason string, out *[]Change) {
	beforeMap, okBefore := before.(map[string]any)
	afterMap, okAfter := after.(map[string]any)

	if okBefore && okAfter {
		keys := make([]string, 0, len(beforeMap)+len(afterMap))
		for k := range beforeMap {
			keys = append(keys, k)
		}

		for k := range afterMap {
			if _, ok := beforeMap[k]; !ok {
				keys = append(keys, k)
			}
		}

		sort.Strings(keys)

		for _, k := range keys {
			b, inBefore := beforeMap[k]
			a, inAfter := afterMap[k]
			childPath := joinPath(path, k)

			switch {
			case !inAfter:
				*out = append(*out, Change{Path: childPath, Old: b, Reason: reason})
			case !inBefore:
				*out = append(*out, Change{Path: childPath, New: a, Reason: reason})
			default:
				diffValues(b, a, childPath, reason, out)
			}
		}

		return
	}

	beforeList, okBefore := before.([]any)
	afterList, okAfter := after.([]any)

	if okBefore && okAfter && len(beforeList) == len(afterList) {
		for i := range beforeList {
			diffValues(beforeList[i], afterList[i], fmt.Sprintf("%s[%d]", path, i), reason, out)
		}

		return
	}

	if !reflect.DeepEqual(before, after) {
		*out = append(*out, Change{Path: path, Old: before, New: after, Reason: reason})
	}
}

func joinPath(path, key string) string {
	if strings.ContainsAny(key, "./[] ") {
		key = fmt.Sprintf("[%q]", key)
		return path + key
	}

	if path == "" {
		return key
	}

	return path + "." + key
}

// FormatValue renders a value compactly on one line, shortened to maxLen.
func FormatValue(v any, maxLen int) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}

	s := string(data)
	if len(s) > maxLen {
		s = s[:maxLen] + "..."
	}

	return s
}
