package normalize

import (
	"bytes"
	"encoding/json"
	"path"
	"strings"
)

func matchesAnyGlob(patterns []string, value string) bool {
	for _, pattern := range patterns {
		if matchGlob(pattern, value) {
			return true
		}
	}

	return false
}

func matchGlob(pattern string, value string) bool {
	matched, err := path.Match(pattern, value)
	if err != nil {
		return false
	}

	return matched
}

func isManagedFieldsPath(segments []string) bool {
	return len(segments) == 2 && segments[0] == fieldMetadata && segments[1] == "managedFields"
}

// deleteField removes the fields matching f from obj, honoring the value,
// omitempty and omitIfEqualToSibling constraints.
func deleteField(obj map[string]any, f ignoreFieldPath) {
	visitFieldPath(obj, f.segments, func(m map[string]any, key string) {
		var remove bool

		switch {
		case f.omitIfEqualToSibling != "":
			sibling, ok := m[f.omitIfEqualToSibling]
			remove = ok && valueMatches(m[key], sibling)
		case f.valueConstrained && f.value == nil:
			remove = m[key] == nil // fast path for the frequent null rules
		case f.valueConstrained:
			remove = valueMatches(m[key], f.value)
		case f.exceptConstrained:
			remove = !valueMatches(m[key], f.exceptValue)
		case f.omitEmpty:
			remove = isEmpty(m[key])
		default:
			remove = true
		}

		if remove {
			delete(m, key)
		}
	})
}

// visitFieldPath calls fn for every map entry matching the path segments:
// a segment is a key, a glob ("*" also matches keys containing "/"), or
// "..." for any depth. Lists are walked implicitly.
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
		pattern := segments[0]

		if !hasFieldSegmentWildcard(pattern) {
			next, ok := typed[pattern]
			if !ok {
				return
			}

			if len(segments) == 1 {
				fn(typed, pattern)
			} else {
				visitFieldPath(next, segments[1:], fn)
			}

			return
		}

		for key, next := range typed {
			if pattern != "*" && !matchGlob(pattern, key) {
				continue
			}

			if len(segments) == 1 {
				fn(typed, key)
			} else {
				visitFieldPath(next, segments[1:], fn)
			}
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

// valueMatches compares the JSON encodings, so 600 (int from the config)
// equals 600 (int64 from the object), but "600" (string) does not.
func valueMatches(actual, expected any) bool {
	a, errA := json.Marshal(actual)
	b, errB := json.Marshal(expected)

	return errA == nil && errB == nil && bytes.Equal(a, b)
}

// isEmpty reports whether v is absent or empty, mirroring json:",omitempty" semantics:
// nil, empty map, empty slice, or empty string are all considered empty.
func isEmpty(v any) bool {
	switch typed := v.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	case map[string]any:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	default:
		return false
	}
}

func hasFieldSegmentWildcard(segment string) bool {
	return strings.ContainsAny(segment, "*?")
}

func getGroup(groupVersion string) string {
	parts := strings.Split(groupVersion, "/")
	if len(parts) > 1 {
		return parts[0]
	}

	return ""
}
