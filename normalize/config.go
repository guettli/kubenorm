package normalize

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"

	yaml "sigs.k8s.io/yaml/goyaml.v3"
)

const deepWildcard = "..."

//go:embed common-config.yaml
var commonConfig []byte

// CommonConfig returns the embedded common config exactly as stored in the
// binary (including comments).
func CommonConfig() []byte {
	return bytes.Clone(commonConfig)
}

// Config holds compiled normalization rules, read from the top-level config
// keys removeFields (fields to remove), skipResources (resources to drop),
// excludeNamespaces (namespaces to drop with everything inside),
// quantityFields (fields to canonicalize as quantities),
// clusterScopedResources (kinds to treat as cluster-scoped), setMetadata
// (labels and annotations to set) and ownerHints (who manages a resource,
// for kubenorm diff). The zero value (or nil) applies no rules.
type Config struct {
	removeRules       []removeRule
	skipRules         []skipRule
	excludeNamespaces []string
	// quantityRules select fields that hold quantities.
	quantityRules []removeRule
	// clusterScopedPattern extends the built-in list of cluster-scoped kinds.
	clusterScopedPattern []groupKindPattern
	setMetadataRules     []setMetadataRule
	ownerHints           []OwnerHint
}

// LoadConfig builds a Config from the embedded common config (if useCommon is
// true) and the given config files, in this order. Empty file names are
// ignored.
func LoadConfig(useCommon bool, files ...string) (*Config, error) {
	cfg := &Config{}

	if useCommon {
		common, err := ParseConfig("common config", commonConfig)
		if err != nil {
			return nil, err
		}

		cfg.merge(common)
	}

	for _, file := range files {
		file = strings.TrimSpace(file)
		if file == "" {
			continue
		}

		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("failed to read config %s: %w", file, err)
		}

		user, err := ParseConfig(file, data)
		if err != nil {
			return nil, err
		}

		cfg.merge(user)
	}

	return cfg, nil
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
// dumpall uses it to skip LIST calls in excluded namespaces.
func (c *Config) ExcludeNamespaces() []string {
	if c == nil {
		return nil
	}

	return slices.Clone(c.excludeNamespaces)
}

// SkipsEveryResourceOf reports whether a skip rule drops every resource of the
// given group and kind, regardless of namespace or name. Callers that list
// resources from an api-server can use this to avoid the LIST call entirely;
// dumpall does so.
func (c *Config) SkipsEveryResourceOf(group, kind string) bool {
	if c == nil {
		return false
	}

	return skipRuleCoversGVR(c.skipRules, group, kind)
}

type configFile struct {
	RemoveFields      []removeRuleFile      `yaml:"removeFields"`
	Skip              []skipRuleFile        `yaml:"skipResources"`
	ExcludeNamespaces []string              `yaml:"excludeNamespaces"`
	ClusterScoped     []groupKindFile       `yaml:"clusterScopedResources"`
	Quantities        []quantityRuleFile    `yaml:"quantityFields"`
	SetMetadata       []setMetadataRuleFile `yaml:"setMetadata"`
	OwnerHints        []OwnerHint           `yaml:"ownerHints"`
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

type removeRuleFile struct {
	Group     *string          `yaml:"group"`
	Kind      *string          `yaml:"kind"`
	Namespace *string          `yaml:"namespace"`
	Name      *string          `yaml:"name"`
	Fields    []fieldEntryFile `yaml:"fields"`
}

// fieldEntryFile is a single entry in the fields list of a removeFields rule.
// It is either a plain field path string, or a map with "path" and optional "value",
// "exceptValue", "omitempty" or "omitIfEqualToSibling" key. These modifiers are mutually exclusive.
type fieldEntryFile struct {
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
	// ExceptValue removes the field unless it has this value (e.g. keep
	// clusterIP: None). Mutually exclusive with the other modifiers.
	ExceptValue       any
	exceptConstrained bool
}

func (e *fieldEntryFile) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		e.Path = value.Value
		return nil
	}

	seen, err := fieldEntryKeys(value)
	if err != nil {
		return err
	}

	var m struct {
		Path                 string `yaml:"path"`
		Value                any    `yaml:"value"`
		ExceptValue          any    `yaml:"exceptValue"`
		OmitEmpty            bool   `yaml:"omitempty"`
		OmitIfEqualToSibling string `yaml:"omitIfEqualToSibling"`
	}

	err = value.Decode(&m)
	if err != nil {
		return fmt.Errorf("failed to decode field entry: %w", err)
	}

	if m.Path == "" {
		return errors.New("field entry map must have a non-empty 'path' key")
	}

	if seen["omitIfEqualToSibling"] && strings.TrimSpace(m.OmitIfEqualToSibling) == "" {
		return errors.New("field entry 'omitIfEqualToSibling' must not be empty")
	}

	*e = fieldEntryFile{
		Path:                 m.Path,
		Value:                m.Value,
		valueConstrained:     seen["value"],
		OmitEmpty:            m.OmitEmpty,
		OmitIfEqualToSibling: m.OmitIfEqualToSibling,
		ExceptValue:          m.ExceptValue,
		exceptConstrained:    seen["exceptValue"],
	}

	return nil
}

// fieldEntryKeys returns the keys of a field entry map and checks that it
// has no unknown keys and at most one modifier.
func fieldEntryKeys(value *yaml.Node) (map[string]bool, error) {
	if value.Kind != yaml.MappingNode {
		return nil, errors.New("field entry must be a string or a map with 'path' and one optional modifier")
	}

	seen := map[string]bool{}
	modifiers := 0

	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i].Value
		switch key {
		case "path":
		case "value", "exceptValue", "omitempty", "omitIfEqualToSibling":
			modifiers++
		default:
			return nil, fmt.Errorf("unknown field %q in field entry (allowed: path, value, exceptValue, omitempty, omitIfEqualToSibling)", key)
		}

		seen[key] = true
	}

	if modifiers > 1 {
		return nil, errors.New("field entry can only set one of 'value', 'exceptValue', 'omitempty', or 'omitIfEqualToSibling'")
	}

	return seen, nil
}

type removeRule struct {
	matcher

	Fields []removeFieldPath
	// Origin describes where the rule comes from, for explain.
	Origin string
}

type removeFieldPath struct {
	// raw is the path as written in the config.
	raw                  string
	segments             []string
	value                any
	valueConstrained     bool
	omitEmpty            bool
	omitIfEqualToSibling string
	exceptValue          any
	exceptConstrained    bool
}

// ParseConfig parses a single YAML config document, like a file given to
// LoadConfig, but without the common config. Unknown keys are rejected. name
// is only used in error messages and in the origin of rules (see explain).
func ParseConfig(name string, data []byte) (*Config, error) {
	err := rejectLegacyConfigKeys(name, data)
	if err != nil {
		return nil, err
	}

	cfg, err := decodeConfigFile(name, data)
	if err != nil {
		return nil, err
	}

	rules, err := compileRemoveRules(name, cfg.RemoveFields)
	if err != nil {
		return nil, err
	}

	skips, err := compileSkipRules(name, cfg.Skip)
	if err != nil {
		return nil, err
	}

	excludes, err := validateExcludeNamespacePatterns(name, cfg.ExcludeNamespaces)
	if err != nil {
		return nil, err
	}

	clusterScoped, err := compileGroupKindPatterns(name, cfg.ClusterScoped)
	if err != nil {
		return nil, err
	}

	quantityRules, err := compileQuantityRules(name, cfg.Quantities)
	if err != nil {
		return nil, err
	}

	setRules, err := compileSetMetadataRules(name, cfg.SetMetadata)
	if err != nil {
		return nil, err
	}

	err = validateOwnerHints(name, cfg.OwnerHints)
	if err != nil {
		return nil, err
	}

	return &Config{
		setMetadataRules:     setRules,
		ownerHints:           cfg.OwnerHints,
		quantityRules:        quantityRules,
		removeRules:          rules,
		skipRules:            skips,
		excludeNamespaces:    excludes,
		clusterScopedPattern: clusterScoped,
	}, nil
}

func compileRemoveRules(name string, files []removeRuleFile) ([]removeRule, error) {
	rules := make([]removeRule, 0, len(files))

	for idx := range files {
		rule, err := compileRemoveRule(files[idx])
		if err != nil {
			return nil, fmt.Errorf("invalid removeFields[%d] in %s: %w", idx+1, name, err)
		}

		rule.Origin = fmt.Sprintf("removeFields[%d] in %s", idx+1, name)
		rules = append(rules, rule)
	}

	return rules, nil
}

func compileSkipRules(name string, files []skipRuleFile) ([]skipRule, error) {
	skips := make([]skipRule, 0, len(files))

	for idx := range files {
		skip, err := compileSkipRule(files[idx])
		if err != nil {
			return nil, fmt.Errorf("invalid skipResources[%d] in %s: %w", idx+1, name, err)
		}

		skip.Origin = fmt.Sprintf("skipResources[%d] in %s", idx+1, name)
		skips = append(skips, skip)
	}

	return skips, nil
}

// decodeIgnoreConfig decodes data, which must be a single YAML document
// without unknown keys.
func decodeConfigFile(name string, data []byte) (configFile, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	var cfg configFile

	err := decoder.Decode(&cfg)
	if err != nil {
		return cfg, fmt.Errorf("failed to parse config %s: %w", name, err)
	}

	var extra any

	err = decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return cfg, nil
	}

	if err == nil {
		return cfg, fmt.Errorf("failed to parse config %s: expected a single YAML document", name)
	}

	return cfg, fmt.Errorf("failed to parse config %s: %w", name, err)
}

func compileQuantityRules(name string, quantities []quantityRuleFile) ([]removeRule, error) {
	quantityRules := make([]removeRule, 0, len(quantities))

	for idx, q := range quantities {
		fields := make([]fieldEntryFile, 0, len(q.Fields))
		for _, f := range q.Fields {
			fields = append(fields, fieldEntryFile{Path: f})
		}

		rule, err := compileRemoveRule(removeRuleFile{Group: q.Group, Kind: q.Kind, Namespace: q.Namespace, Name: q.Name, Fields: fields})
		if err != nil {
			return nil, fmt.Errorf("invalid quantityFields[%d] in %s: %w", idx+1, name, err)
		}

		rule.Origin = fmt.Sprintf("quantityFields[%d] in %s", idx+1, name)
		quantityRules = append(quantityRules, rule)
	}

	return quantityRules, nil
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

func compileRemoveRule(fileRule removeRuleFile) (removeRule, error) {
	fields := make([]removeFieldPath, 0, len(fileRule.Fields))
	for _, field := range fileRule.Fields {
		trimmed := strings.TrimSpace(field.Path)
		if trimmed == "" {
			return removeRule{}, fmt.Errorf("fields must not contain empty entries")
		}

		segments, err := parseFieldPath(trimmed)
		if err != nil {
			return removeRule{}, fmt.Errorf("invalid field path %q: %w", trimmed, err)
		}

		for _, segment := range segments {
			if segment == deepWildcard || !hasFieldSegmentWildcard(segment) {
				continue
			}

			err := validateGlobPattern(segment)
			if err != nil {
				return removeRule{}, fmt.Errorf("invalid field path %q: invalid segment glob %q: %w", trimmed, segment, err)
			}
		}

		fields = append(fields, removeFieldPath{
			raw:                  trimmed,
			segments:             segments,
			value:                field.Value,
			valueConstrained:     field.valueConstrained,
			omitEmpty:            field.OmitEmpty,
			omitIfEqualToSibling: field.OmitIfEqualToSibling,
			exceptValue:          field.ExceptValue,
			exceptConstrained:    field.exceptConstrained,
		})
	}

	if len(fields) == 0 {
		return removeRule{}, fmt.Errorf("fields must not be empty")
	}

	m, err := compileMatcher(fileRule.Group, fileRule.Kind, fileRule.Namespace, fileRule.Name)
	if err != nil {
		return removeRule{}, err
	}

	return removeRule{matcher: m, Fields: fields}, nil
}

// rejectLegacyIgnoreConfigKeys catches configs that still use the old
// top-level keys (`rules:` / `skip:`) and reports a clear rename hint instead
// of the generic "field not found in type" error from strict parsing.
func rejectLegacyConfigKeys(name string, data []byte) error {
	var preview map[string]any

	if yaml.Unmarshal(data, &preview) == nil {
		legacy := []struct {
			oldKey string
			newKey string
		}{
			{"rules", "removeFields"},
			{"skip", "skipResources"},
		}

		for _, entry := range legacy {
			if _, ok := preview[entry.oldKey]; ok {
				return fmt.Errorf("config %s uses legacy key %q — rename it to %q", name, entry.oldKey, entry.newKey)
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

func parseFieldPath(fieldPath string) ([]string, error) {
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
		case strings.HasPrefix(fieldPath[i:], deepWildcard):
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

// merge appends all rules of other to c.
func (c *Config) merge(other *Config) {
	if other == nil {
		return
	}

	c.removeRules = append(c.removeRules, other.removeRules...)
	c.skipRules = append(c.skipRules, other.skipRules...)
	c.excludeNamespaces = append(c.excludeNamespaces, other.excludeNamespaces...)
	c.clusterScopedPattern = append(c.clusterScopedPattern, other.clusterScopedPattern...)
	c.quantityRules = append(c.quantityRules, other.quantityRules...)
	c.setMetadataRules = append(c.setMetadataRules, other.setMetadataRules...)
	c.ownerHints = append(c.ownerHints, other.ownerHints...)
}
