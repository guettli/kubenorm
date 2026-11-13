package normalize

import (
	"fmt"
	"path"
	"strings"
)

func (r ignoreRule) matches(identity Identity) bool {
	return matchGlob(r.GroupPattern, identity.Group) &&
		matchGlob(r.KindPattern, identity.Kind) &&
		matchGlob(r.NamespacePattern, identity.Namespace) &&
		matchGlob(r.NamePattern, identity.Name)
}

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

func deleteFieldPath(current any, segments []string, constraintValue any, constrained bool, omitEmpty bool, omitIfEqualToSibling string) {
	if len(segments) == 0 {
		return
	}

	if segments[0] == deepWildcard {
		deleteFieldPathRecursive(current, segments[1:], constraintValue, constrained, omitEmpty, omitIfEqualToSibling)
		return
	}

	switch typed := current.(type) {
	case map[string]any:
		deleteFieldPathFromMap(typed, segments, constraintValue, constrained, omitEmpty, omitIfEqualToSibling)
	case map[string]string:
		deleteFieldPathFromStringMap(typed, segments, constraintValue, constrained, omitEmpty)
	case []any:
		for _, entry := range typed {
			deleteFieldPath(entry, segments, constraintValue, constrained, omitEmpty, omitIfEqualToSibling)
		}
	case []map[string]any:
		for _, entry := range typed {
			deleteFieldPath(entry, segments, constraintValue, constrained, omitEmpty, omitIfEqualToSibling)
		}
	case []map[string]string:
		for _, entry := range typed {
			deleteFieldPath(entry, segments, constraintValue, constrained, omitEmpty, omitIfEqualToSibling)
		}
	}
}

func deleteFieldPathRecursive(current any, remaining []string, constraintValue any, constrained bool, omitEmpty bool, omitIfEqualToSibling string) {
	if len(remaining) == 0 {
		return
	}

	deleteFieldPath(current, remaining, constraintValue, constrained, omitEmpty, omitIfEqualToSibling)

	switch typed := current.(type) {
	case map[string]any:
		for _, value := range typed {
			deleteFieldPathRecursive(value, remaining, constraintValue, constrained, omitEmpty, omitIfEqualToSibling)
		}
	case []any:
		for _, entry := range typed {
			deleteFieldPathRecursive(entry, remaining, constraintValue, constrained, omitEmpty, omitIfEqualToSibling)
		}
	case []map[string]any:
		for _, entry := range typed {
			deleteFieldPathRecursive(entry, remaining, constraintValue, constrained, omitEmpty, omitIfEqualToSibling)
		}
	case []map[string]string:
		for _, entry := range typed {
			deleteFieldPathRecursive(entry, remaining, constraintValue, constrained, omitEmpty, omitIfEqualToSibling)
		}
	}
}

func shouldDeleteEntry(value, constraintValue any, constrained, omitEmpty bool) bool {
	if constrained && !valueMatches(value, constraintValue) {
		return false
	}

	if omitEmpty && !isEmpty(value) {
		return false
	}

	return true
}

func shouldDeleteLeaf(current map[string]any, key string, constraintValue any, constrained bool, omitEmpty bool, omitIfEqualToSibling string) bool {
	if omitIfEqualToSibling != "" {
		sibling, ok := current[omitIfEqualToSibling]
		return ok && valueMatches(current[key], sibling)
	}

	return shouldDeleteEntry(current[key], constraintValue, constrained, omitEmpty)
}

func deleteFieldPathFromMap(current map[string]any, segments []string, constraintValue any, constrained bool, omitEmpty bool, omitIfEqualToSibling string) {
	keyPattern := segments[0]
	if !hasFieldSegmentWildcard(keyPattern) {
		if len(segments) == 1 {
			if shouldDeleteLeaf(current, keyPattern, constraintValue, constrained, omitEmpty, omitIfEqualToSibling) {
				delete(current, keyPattern)
			}

			return
		}

		next, ok := current[keyPattern]
		if !ok {
			return
		}

		deleteFieldPath(next, segments[1:], constraintValue, constrained, omitEmpty, omitIfEqualToSibling)

		return
	}

	for key, next := range current {
		if !matchGlob(keyPattern, key) {
			continue
		}

		if len(segments) == 1 {
			if shouldDeleteLeaf(current, key, constraintValue, constrained, omitEmpty, omitIfEqualToSibling) {
				delete(current, key)
			}

			continue
		}

		deleteFieldPath(next, segments[1:], constraintValue, constrained, omitEmpty, omitIfEqualToSibling)
	}
}

func deleteFieldPathFromStringMap(current map[string]string, segments []string, constraintValue any, constrained bool, omitEmpty bool) {
	if len(segments) != 1 {
		return
	}

	keyPattern := segments[0]
	if !hasFieldSegmentWildcard(keyPattern) {
		if shouldDeleteEntry(current[keyPattern], constraintValue, constrained, omitEmpty) {
			delete(current, keyPattern)
		}

		return
	}

	for key := range current {
		if matchGlob(keyPattern, key) && shouldDeleteEntry(current[key], constraintValue, constrained, omitEmpty) {
			delete(current, key)
		}
	}
}

func valueMatches(actual, expected any) bool {
	return fmt.Sprintf("%v", actual) == fmt.Sprintf("%v", expected)
}

// isEmpty reports whether v is absent or empty, mirroring json:",omitempty" semantics:
// nil, empty map, empty slice, or empty string are all considered empty.
func isEmpty(v any) bool {
	if v == nil {
		return true
	}

	switch typed := v.(type) {
	case string:
		return typed == ""
	case map[string]any:
		return len(typed) == 0
	case map[string]string:
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
