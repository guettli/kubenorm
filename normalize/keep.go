package normalize

import (
	"fmt"
	"slices"
	"strings"
)

// keepRuleFile is an entry of keepFields: fields that no removeFields rule
// removes, also not one of the common config.
type keepRuleFile struct {
	Group     *string  `yaml:"group"`
	Kind      *string  `yaml:"kind"`
	Namespace *string  `yaml:"namespace"`
	Name      *string  `yaml:"name"`
	Fields    []string `yaml:"fields"`
}

type keepRule struct {
	matcher

	Fields []keepPath
}

type keepPath struct {
	raw      string
	segments []string
}

// keptValue is a field to restore after the remove rules: its concrete
// path (map keys and list indexes) and its value.
type keptValue struct {
	path  []any
	value any
}

// AddKeepField protects a field of every resource from the removeFields
// rules, like a keepFields rule with only fields. The path uses the syntax
// of the config (dots, globs, "..."). An empty path is a no-op.
func (c *Config) AddKeepField(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}

	rule, err := compileKeepRule(keepRuleFile{Fields: []string{path}})
	if err != nil {
		return fmt.Errorf("invalid keep-field %q: %w", path, err)
	}

	c.keepRules = append(c.keepRules, rule)

	return nil
}

func compileKeepRules(name string, files []keepRuleFile) ([]keepRule, error) {
	rules := make([]keepRule, 0, len(files))

	for idx, file := range files {
		rule, err := compileKeepRule(file)
		if err != nil {
			return nil, fmt.Errorf("invalid keepFields[%d] in %s: %w", idx+1, name, err)
		}

		rules = append(rules, rule)
	}

	return rules, nil
}

func compileKeepRule(file keepRuleFile) (keepRule, error) {
	if len(file.Fields) == 0 {
		return keepRule{}, fmt.Errorf("fields must not be empty")
	}

	fields := make([]keepPath, 0, len(file.Fields))

	for _, field := range file.Fields {
		field = strings.TrimSpace(field)

		segments, err := compileFieldPath(field)
		if err != nil {
			return keepRule{}, err
		}

		fields = append(fields, keepPath{raw: field, segments: segments})
	}

	m, err := compileMatcher(file.Group, file.Kind, file.Namespace, file.Name)
	if err != nil {
		return keepRule{}, err
	}

	return keepRule{matcher: m, Fields: fields}, nil
}

// compileFieldPath parses a field path of the config and validates its globs.
func compileFieldPath(field string) ([]string, error) {
	if field == "" {
		return nil, fmt.Errorf("fields must not contain empty entries")
	}

	segments, err := parseFieldPath(field)
	if err != nil {
		return nil, fmt.Errorf("invalid field path %q: %w", field, err)
	}

	for _, segment := range segments {
		if segment == deepWildcard || !hasFieldSegmentWildcard(segment) {
			continue
		}

		err := validateGlobPattern(segment)
		if err != nil {
			return nil, fmt.Errorf("invalid field path %q: invalid segment glob %q: %w", field, segment, err)
		}
	}

	return segments, nil
}

// keptValues returns the fields of obj that the keep rules protect.
func (c *Config) keptValues(obj map[string]any, identity objectIdentity) []keptValue {
	var kept []keptValue

	for _, rule := range c.keepRules {
		if !rule.matches(identity) {
			continue
		}

		for _, field := range rule.Fields {
			visitPaths(obj, field.segments, nil, func(path []any, value any) {
				// A reference, not a copy: rules that remove children
				// before the parent change the kept value too.
				kept = append(kept, keptValue{path: path, value: value})
			})
		}
	}

	return kept
}

// visitPaths is visitFieldPath, but passes the concrete path and the value.
func visitPaths(current any, segments []string, prefix []any, fn func(path []any, value any)) {
	if len(segments) == 0 {
		return
	}

	if segments[0] == deepWildcard {
		visitPathsRecursive(current, segments[1:], prefix, fn)
		return
	}

	switch typed := current.(type) {
	case map[string]any:
		pattern := segments[0]

		if !hasFieldSegmentWildcard(pattern) {
			if next, ok := typed[pattern]; ok {
				visitPathEntry(next, segments, append(slices.Clone(prefix), pattern), fn)
			}

			return
		}

		for key, next := range typed {
			if pattern != "*" && pattern != key && (!hasFieldSegmentWildcard(pattern) || !matchGlob(pattern, key)) {
				continue
			}

			visitPathEntry(next, segments, append(slices.Clone(prefix), key), fn)
		}
	case []any:
		for i, entry := range typed {
			visitPaths(entry, segments, append(slices.Clone(prefix), i), fn)
		}
	}
}

// visitPathEntry continues visitPaths at the map entry value, found at path
// for segments[0].
func visitPathEntry(value any, segments []string, path []any, fn func(path []any, value any)) {
	if len(segments) == 1 {
		fn(path, value)
	} else {
		visitPaths(value, segments[1:], path, fn)
	}
}

func visitPathsRecursive(current any, remaining []string, prefix []any, fn func(path []any, value any)) {
	if len(remaining) == 0 {
		return
	}

	switch typed := current.(type) {
	case map[string]any:
		visitPaths(current, remaining, prefix, fn)

		for key, value := range typed {
			visitPathsRecursive(value, remaining, append(slices.Clone(prefix), key), fn)
		}
	case []any:
		// Not visitPaths for the list: it would visit the entries twice.
		for i, entry := range typed {
			visitPathsRecursive(entry, remaining, append(slices.Clone(prefix), i), fn)
		}
	}
}

// restoreKept puts back kept fields that a remove rule deleted, creating
// the maps on the way. A field inside a removed list is not restored.
func restoreKept(obj map[string]any, kept []keptValue) {
	for _, k := range kept {
		restorePath(obj, k.path, k.value)
	}
}

func restorePath(current any, path []any, value any) {
	switch key := path[0].(type) {
	case string:
		m, ok := current.(map[string]any)
		if !ok {
			return
		}

		if len(path) == 1 {
			if _, ok := m[key]; !ok {
				m[key] = value
			}

			return
		}

		next, ok := m[key]
		if !ok {
			if _, isIndex := path[1].(int); isIndex {
				return
			}

			next = map[string]any{}
			m[key] = next
		}

		restorePath(next, path[1:], value)
	case int:
		l, ok := current.([]any)
		if !ok || key >= len(l) {
			return
		}

		if len(path) == 1 {
			return // list entries are never removed on their own
		}

		restorePath(l[key], path[1:], value)
	}
}
