package normalize

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	yaml "sigs.k8s.io/yaml/goyaml.v3"
)

const (
	deepWildcard = "..."
	fieldRules   = "rules"
)

//go:embed common-config.yaml
var commonConfig []byte

// CommonConfig returns the embedded common config exactly as stored in the
// binary (including comments).
func CommonConfig() []byte {
	return bytes.Clone(commonConfig)
}

// Config holds compiled normalization rules: fields to remove, resources to
// skip and namespaces to exclude. The zero value (or nil) applies no rules.
type Config struct {
	removeRules       []ignoreRule
	skipRules         []skipRule
	excludeNamespaces []string
	// quantityRules select fields that hold quantities.
	quantityRules []ignoreRule
	// clusterScopedPattern extends the built-in list of cluster-scoped kinds.
	clusterScopedPattern []groupKindPattern
}

// LoadConfig builds a Config from the embedded common config (if useCommon is
// true) and the optional user config file. Both are merged.
func LoadConfig(useCommon bool, userConfigFile string) (*Config, error) {
	cfg := &Config{}

	if useCommon {
		common, err := parseConfig("common config", commonConfig)
		if err != nil {
			return nil, err
		}

		cfg.Merge(common)
	}

	trimmedFileName := strings.TrimSpace(userConfigFile)
	if trimmedFileName == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(trimmedFileName)
	if err != nil {
		return nil, fmt.Errorf("failed to read config %s: %w", trimmedFileName, err)
	}

	user, err := parseConfig(trimmedFileName, data)
	if err != nil {
		return nil, err
	}

	cfg.Merge(user)

	return cfg, nil
}

// parseConfig parses a single YAML config document. Unknown fields are
// rejected (strict mode). name is only used in error messages.
func parseConfig(name string, data []byte) (*Config, error) {
	return parseIgnoreConfigBytes(name, data)
}

// Merge appends all rules of other to c.
func (c *Config) Merge(other *Config) {
	if other == nil {
		return
	}

	c.removeRules = append(c.removeRules, other.removeRules...)
	c.skipRules = append(c.skipRules, other.skipRules...)
	c.excludeNamespaces = append(c.excludeNamespaces, other.excludeNamespaces...)
	c.clusterScopedPattern = append(c.clusterScopedPattern, other.clusterScopedPattern...)
	c.quantityRules = append(c.quantityRules, other.quantityRules...)
}

// AddSkipNameGlob adds a skip rule matching resources whose metadata.name
// matches the anchored glob. An empty glob is a no-op.
func (c *Config) AddSkipNameGlob(glob string) error {
	if strings.TrimSpace(glob) == "" {
		return nil
	}

	rule, err := compileSkipRule(skipRuleFile{Name: &glob})
	if err != nil {
		return fmt.Errorf("invalid skip-name-glob: %w", err)
	}

	rule.Origin = "--skip-name-glob " + glob
	c.skipRules = append(c.skipRules, rule)

	return nil
}

// AddExcludeNamespacesCSV adds a comma-separated list of namespace globs to
// exclude. An empty string is a no-op.
func (c *Config) AddExcludeNamespacesCSV(csv string) error {
	patterns, err := parseExcludeNamespacesCSV(csv)
	if err != nil {
		return err
	}

	c.excludeNamespaces = append(c.excludeNamespaces, patterns...)

	return nil
}

// ExcludeNamespaces returns the namespace globs that are fully excluded.
func (c *Config) ExcludeNamespaces() []string {
	if c == nil {
		return nil
	}

	return slicesClone(c.excludeNamespaces)
}

// HasRemoveRules reports whether the config contains at least one removeFields rule.
func (c *Config) HasRemoveRules() bool {
	return c != nil && len(c.removeRules) > 0
}

// SkipsEveryResourceOf reports whether a skip rule drops every resource of the
// given group and kind, regardless of namespace or name. Callers that list
// resources from an api-server can use this to avoid the LIST call entirely.
func (c *Config) SkipsEveryResourceOf(group, kind string) bool {
	if c == nil {
		return false
	}

	return skipRuleCoversGVR(c.skipRules, group, kind)
}

func slicesClone(s []string) []string {
	if s == nil {
		return nil
	}

	return append([]string(nil), s...)
}

type ignoreConfig struct {
	Rules             []ignoreRuleFile   `yaml:"removeFields"`
	Skip              []skipRuleFile     `yaml:"skipResources"`
	ExcludeNamespaces []string           `yaml:"excludeNamespaces"`
	ClusterScoped     []groupKindFile    `yaml:"clusterScopedResources"`
	Quantities        []quantityRuleFile `yaml:"quantityFields"`
}

// quantityRuleFile selects fields whose values are Kubernetes quantities
// (cpu, memory, storage, ...). They are rewritten to the canonical form the
// api-server returns: 1000m -> "1", 0.5 -> 500m, 1024Mi -> 1Gi.
type quantityRuleFile struct {
	Group     *string  `yaml:"group"`
	Kind      *string  `yaml:"kind"`
	Namespace *string  `yaml:"namespace"`
	Name      *string  `yaml:"name"`
	Fields    []string `yaml:"fields"`
}

type skipRuleFile struct {
	Group     *string `yaml:"group"`
	Kind      *string `yaml:"kind"`
	Namespace *string `yaml:"namespace"`
	Name      *string `yaml:"name"`
}

// matcher selects resources by group, kind, namespace and name globs.
type matcher struct {
	GroupPattern     string
	KindPattern      string
	NamespacePattern string
	NamePattern      string
}

func (m matcher) matches(identity objectIdentity) bool {
	return matchGlob(m.GroupPattern, identity.Group) &&
		matchGlob(m.KindPattern, identity.Kind) &&
		matchGlob(m.NamespacePattern, identity.Namespace) &&
		matchGlob(m.NamePattern, identity.Name)
}

// compileMatcher validates the globs. Omitted matchers become "*".
func compileMatcher(group, kind, namespace, name *string) (matcher, error) {
	m := matcher{
		GroupPattern:     patternOrWildcard(group),
		KindPattern:      patternOrWildcard(kind),
		NamespacePattern: patternOrWildcard(namespace),
		NamePattern:      patternOrWildcard(name),
	}

	for _, pattern := range []string{m.GroupPattern, m.KindPattern, m.NamespacePattern, m.NamePattern} {
		err := validateGlobPattern(pattern)
		if err != nil {
			return matcher{}, fmt.Errorf("invalid glob %q: %w", pattern, err)
		}
	}

	return m, nil
}

type skipRule struct {
	matcher

	// Origin describes where the rule comes from, for explain.
	Origin string
}

type ignoreRuleFile struct {
	Group     *string                `yaml:"group"`
	Kind      *string                `yaml:"kind"`
	Namespace *string                `yaml:"namespace"`
	Name      *string                `yaml:"name"`
	Fields    []ignoreFieldEntryFile `yaml:"fields"`
}

// ignoreFieldEntryFile is a single entry in the fields list of a removeFields rule.
// It is either a plain field path string, or a map with "path" and optional "value",
// "omitempty", or "omitIfEqualToSibling" keys. All three optional keys are mutually exclusive.
type ignoreFieldEntryFile struct {
	Path             string
	Value            any
	valueConstrained bool
	// OmitEmpty removes the field only when its value is nil, an empty map, an empty
	// slice, or an empty string — mirroring the json:",omitempty" / kubebuilder
	// +optional convention. Mutually exclusive with a value constraint.
	OmitEmpty bool
	// OmitIfEqualToSibling removes the field only when its value equals the value of
	// the named sibling field in the same map object. Useful for defaulted fields like
	// spec.ports.targetPort which Kubernetes sets to the same value as spec.ports.port.
	// Mutually exclusive with value and omitempty.
	OmitIfEqualToSibling string
}

func (e *ignoreFieldEntryFile) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		e.Path = value.Value
		return nil
	case yaml.MappingNode:
		hasValue, hasOmitEmpty, hasOmitIfEqualToSibling := false, false, false

		for i := 0; i < len(value.Content)-1; i += 2 {
			key := value.Content[i].Value
			switch key {
			case "path":
			case "value":
				hasValue = true
			case "omitempty":
				hasOmitEmpty = true
			case "omitIfEqualToSibling":
				hasOmitIfEqualToSibling = true
			default:
				return fmt.Errorf("unknown field %q in field entry (allowed: path, value, omitempty, omitIfEqualToSibling)", key)
			}
		}

		modifiers := 0

		for _, has := range []bool{hasValue, hasOmitEmpty, hasOmitIfEqualToSibling} {
			if has {
				modifiers++
			}
		}

		if modifiers > 1 {
			return fmt.Errorf("field entry can only set one of 'value', 'omitempty', or 'omitIfEqualToSibling'")
		}

		var m struct {
			Path                 string `yaml:"path"`
			Value                any    `yaml:"value"`
			OmitEmpty            bool   `yaml:"omitempty"`
			OmitIfEqualToSibling string `yaml:"omitIfEqualToSibling"`
		}

		err := value.Decode(&m)
		if err != nil {
			return fmt.Errorf("failed to decode field entry: %w", err)
		}

		if m.Path == "" {
			return fmt.Errorf("field entry map must have a non-empty 'path' key")
		}

		e.Path = m.Path
		if hasValue {
			e.Value = m.Value
			e.valueConstrained = true
		}

		e.OmitEmpty = m.OmitEmpty

		if hasOmitIfEqualToSibling {
			if strings.TrimSpace(m.OmitIfEqualToSibling) == "" {
				return fmt.Errorf("field entry 'omitIfEqualToSibling' must not be empty")
			}

			e.OmitIfEqualToSibling = m.OmitIfEqualToSibling
		}

		return nil
	case yaml.DocumentNode, yaml.SequenceNode, yaml.AliasNode:
		return fmt.Errorf("field entry must be a string or a map with 'path', 'value', or 'omitempty' keys")
	}

	return fmt.Errorf("field entry must be a string or a map with 'path', 'value', or 'omitempty' keys")
}

type ignoreRule struct {
	matcher

	Fields []ignoreFieldPath
	// Origin describes where the rule comes from, for explain.
	Origin string
}

type ignoreFieldPath struct {
	// raw is the path as written in the config.
	raw                  string
	segments             []string
	value                any
	valueConstrained     bool
	omitEmpty            bool
	omitIfEqualToSibling string
}

func parseIgnoreConfigBytes(name string, data []byte) (*Config, error) {
	err := rejectLegacyIgnoreConfigKeys(name, data)
	if err != nil {
		return nil, err
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	var cfg ignoreConfig

	err = decoder.Decode(&cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ignore config %s: %w", name, err)
	}

	var extra any

	err = decoder.Decode(&extra)
	if !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("failed to parse ignore config %s: expected a single YAML document", name)
		}

		return nil, fmt.Errorf("failed to parse ignore config %s: %w", name, err)
	}

	rules := make([]ignoreRule, 0, len(cfg.Rules))
	for idx := range cfg.Rules {
		rule, err := compileIgnoreRule(cfg.Rules[idx])
		if err != nil {
			return nil, fmt.Errorf("invalid ignore rule %d in %s: %w", idx+1, name, err)
		}

		rule.Origin = fmt.Sprintf("removeFields[%d] in %s", idx+1, name)
		rules = append(rules, rule)
	}

	skips := make([]skipRule, 0, len(cfg.Skip))
	for idx := range cfg.Skip {
		skip, err := compileSkipRule(cfg.Skip[idx])
		if err != nil {
			return nil, fmt.Errorf("invalid skip rule %d in %s: %w", idx+1, name, err)
		}

		skip.Origin = fmt.Sprintf("skipResources[%d] in %s", idx+1, name)
		skips = append(skips, skip)
	}

	excludes, err := validateExcludeNamespacePatterns(name, cfg.ExcludeNamespaces)
	if err != nil {
		return nil, err
	}

	clusterScoped, err := compileGroupKindPatterns(name, cfg.ClusterScoped)
	if err != nil {
		return nil, err
	}

	quantityRules := make([]ignoreRule, 0, len(cfg.Quantities))

	for idx, q := range cfg.Quantities {
		fields := make([]ignoreFieldEntryFile, 0, len(q.Fields))
		for _, f := range q.Fields {
			fields = append(fields, ignoreFieldEntryFile{Path: f})
		}

		rule, err := compileIgnoreRule(ignoreRuleFile{Group: q.Group, Kind: q.Kind, Namespace: q.Namespace, Name: q.Name, Fields: fields})
		if err != nil {
			return nil, fmt.Errorf("invalid quantityFields rule %d in %s: %w", idx+1, name, err)
		}

		rule.Origin = fmt.Sprintf("quantityFields[%d] in %s", idx+1, name)
		quantityRules = append(quantityRules, rule)
	}

	return &Config{
		quantityRules:        quantityRules,
		removeRules:          rules,
		skipRules:            skips,
		excludeNamespaces:    excludes,
		clusterScopedPattern: clusterScoped,
	}, nil
}

func parseExcludeNamespacesCSV(csv string) ([]string, error) {
	trimmed := strings.TrimSpace(csv)
	if trimmed == "" {
		return nil, nil
	}

	patterns := make([]string, 0)

	for entry := range strings.SplitSeq(csv, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		patterns = append(patterns, entry)
	}

	if len(patterns) == 0 {
		return nil, fmt.Errorf("invalid --exclude-namespaces %q: no patterns found", csv)
	}

	return validateExcludeNamespacePatterns("--exclude-namespaces", patterns)
}

func validateExcludeNamespacePatterns(source string, patterns []string) ([]string, error) {
	out := make([]string, 0, len(patterns))
	for idx, pattern := range patterns {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			return nil, fmt.Errorf("invalid excludeNamespaces entry %d in %s: empty pattern", idx+1, source)
		}

		err := validateGlobPattern(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid excludeNamespaces entry %d in %s: invalid glob %q: %w", idx+1, source, trimmed, err)
		}

		out = append(out, trimmed)
	}

	return out, nil
}

func compileIgnoreRule(fileRule ignoreRuleFile) (ignoreRule, error) {
	fields := make([]ignoreFieldPath, 0, len(fileRule.Fields))
	for _, field := range fileRule.Fields {
		trimmed := strings.TrimSpace(field.Path)
		if trimmed == "" {
			return ignoreRule{}, fmt.Errorf("fields must not contain empty entries")
		}

		segments, err := parseIgnoreFieldPath(trimmed)
		if err != nil {
			return ignoreRule{}, fmt.Errorf("invalid field path %q: %w", trimmed, err)
		}

		for _, segment := range segments {
			if segment == deepWildcard || !hasFieldSegmentWildcard(segment) {
				continue
			}

			err := validateGlobPattern(segment)
			if err != nil {
				return ignoreRule{}, fmt.Errorf("invalid field path %q: invalid segment glob %q: %w", trimmed, segment, err)
			}
		}

		fields = append(fields, ignoreFieldPath{
			raw:                  trimmed,
			segments:             segments,
			value:                field.Value,
			valueConstrained:     field.valueConstrained,
			omitEmpty:            field.OmitEmpty,
			omitIfEqualToSibling: field.OmitIfEqualToSibling,
		})
	}

	if len(fields) == 0 {
		return ignoreRule{}, fmt.Errorf("fields must not be empty")
	}

	m, err := compileMatcher(fileRule.Group, fileRule.Kind, fileRule.Namespace, fileRule.Name)
	if err != nil {
		return ignoreRule{}, err
	}

	return ignoreRule{matcher: m, Fields: fields}, nil
}

// rejectLegacyIgnoreConfigKeys catches configs that still use the old
// top-level keys (`rules:` / `skip:`) and reports a clear rename hint instead
// of the generic "field not found in type" error from strict parsing.
func rejectLegacyIgnoreConfigKeys(name string, data []byte) error {
	var preview map[string]any

	if yaml.Unmarshal(data, &preview) == nil {
		legacy := []struct {
			oldKey string
			newKey string
		}{
			{fieldRules, "removeFields"},
			{"skip", "skipResources"},
		}

		for _, entry := range legacy {
			if _, ok := preview[entry.oldKey]; ok {
				return fmt.Errorf("ignore config %s uses legacy key %q — rename it to %q", name, entry.oldKey, entry.newKey)
			}
		}
	}

	return nil
}

func compileSkipRule(fileRule skipRuleFile) (skipRule, error) {
	if !skipRuleHasMatcher(fileRule) {
		return skipRule{}, fmt.Errorf("skip rule must set at least one of group, kind, namespace, name")
	}

	m, err := compileMatcher(fileRule.Group, fileRule.Kind, fileRule.Namespace, fileRule.Name)
	if err != nil {
		return skipRule{}, err
	}

	return skipRule{matcher: m}, nil
}

func skipRuleHasMatcher(fileRule skipRuleFile) bool {
	for _, value := range []*string{fileRule.Group, fileRule.Kind, fileRule.Namespace, fileRule.Name} {
		if value != nil && strings.TrimSpace(*value) != "" {
			return true
		}
	}

	return false
}

// skipRuleCoversGVR reports whether any rule would drop every resource of the
// given group+kind regardless of namespace or name. When this holds, the LIST
// call for that GVR can be skipped entirely — both as an optimization and to
// avoid noisy "forbidden" warnings on clusters where the caller lacks list
// permission for a kind the user already asked to skip.
func skipRuleCoversGVR(rules []skipRule, group string, kind string) bool {
	for _, rule := range rules {
		if rule.NamespacePattern != "*" || rule.NamePattern != "*" {
			continue
		}

		if matchGlob(rule.GroupPattern, group) && matchGlob(rule.KindPattern, kind) {
			return true
		}
	}

	return false
}

func patternOrWildcard(value *string) string {
	if value == nil {
		return "*"
	}

	return strings.TrimSpace(*value)
}

func validateGlobPattern(pattern string) error {
	_, err := path.Match(pattern, "")
	if err != nil {
		return fmt.Errorf("invalid glob pattern: %w", err)
	}

	return nil
}

func parseIgnoreFieldPath(fieldPath string) ([]string, error) {
	var segments []string

	var current strings.Builder

	escaped := false

	for i := 0; i < len(fieldPath); i++ {
		r := fieldPath[i]

		switch {
		case escaped:
			current.WriteByte(r)

			escaped = false
		case r == '\\':
			escaped = true
		case fieldPath[i:] == deepWildcard || strings.HasPrefix(fieldPath[i:], deepWildcard):
			if current.Len() > 0 {
				segments = append(segments, current.String())
				current.Reset()
			}

			segments = append(segments, deepWildcard)
			i += 2
		case r == '.':
			if current.Len() == 0 {
				return nil, fmt.Errorf("empty path segment")
			}

			segments = append(segments, current.String())
			current.Reset()
		default:
			current.WriteByte(r)
		}
	}

	if escaped {
		return nil, fmt.Errorf("path ends with an unfinished escape")
	}

	if current.Len() == 0 {
		if len(segments) > 0 && segments[len(segments)-1] == deepWildcard {
			return nil, fmt.Errorf("path must not end with the '...' wildcard segment")
		}

		return nil, fmt.Errorf("empty path segment")
	}

	segments = append(segments, current.String())

	return segments, nil
}
