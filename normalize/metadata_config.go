package normalize

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// Names of the metadata maps in error messages.
const (
	kindLabel      = "label"
	kindAnnotation = "annotation"
)

// setMetadataRuleFile sets labels and annotations on the selected resources,
// see the setMetadata key of the config.
type setMetadataRuleFile struct {
	Group       *string            `yaml:"group"`
	Kind        *string            `yaml:"kind"`
	Namespace   *string            `yaml:"namespace"`
	Name        *string            `yaml:"name"`
	Labels      map[string]*string `yaml:"labels"`
	Annotations map[string]*string `yaml:"annotations"`
}

type setMetadataRule struct {
	matcher

	labels      map[string]string
	annotations map[string]string
	// Origin describes where the rule comes from, for explain.
	Origin string
}

func compileSetMetadataRules(name string, files []setMetadataRuleFile) ([]setMetadataRule, error) {
	rules := make([]setMetadataRule, 0, len(files))

	for idx, f := range files {
		rule, err := compileSetMetadataRule(f)
		if err != nil {
			return nil, fmt.Errorf("invalid setMetadata[%d] in %s: %w", idx+1, name, err)
		}

		rule.Origin = fmt.Sprintf("setMetadata[%d] in %s", idx+1, name)
		rules = append(rules, rule)
	}

	return rules, nil
}

func compileSetMetadataRule(f setMetadataRuleFile) (setMetadataRule, error) {
	m, err := compileMatcher(f.Group, f.Kind, f.Namespace, f.Name)
	if err != nil {
		return setMetadataRule{}, err
	}

	if len(f.Labels) == 0 && len(f.Annotations) == 0 {
		return setMetadataRule{}, errors.New("set labels or annotations")
	}

	labels, err := nonNullValues(kindLabel, f.Labels)
	if err != nil {
		return setMetadataRule{}, err
	}

	annotations, err := nonNullValues(kindAnnotation, f.Annotations)
	if err != nil {
		return setMetadataRule{}, err
	}

	err = validateMetadataValues(labels, annotations)
	if err != nil {
		return setMetadataRule{}, err
	}

	return setMetadataRule{matcher: m, labels: labels, annotations: annotations}, nil
}

// nonNullValues rejects null values: "team:" without a value is likely a
// typo. Write "team: ”" for an empty value.
func nonNullValues(what string, values map[string]*string) (map[string]string, error) {
	out := make(map[string]string, len(values))

	for k, v := range values {
		if v == nil {
			return nil, fmt.Errorf("%s %q has no value (write '' for an empty value)", what, k)
		}

		out[k] = *v
	}

	return out, nil
}

// applySetMetadataRules sets the labels and annotations of the matching
// setMetadata rules. It runs after the remove rules, like SetLabels.
func (o *Options) applySetMetadataRules(obj, metadata map[string]any, t *tracer) {
	if o.Config == nil || len(o.Config.setMetadataRules) == 0 {
		return
	}

	identity := identityFromObject(obj)

	for _, rule := range o.Config.setMetadataRules {
		if !rule.matches(identity) {
			continue
		}

		setMetadataMap(obj, metadata, "labels", rule.Origin+":", rule.labels, t)
		setMetadataMap(obj, metadata, "annotations", rule.Origin+":", rule.annotations, t)
	}
}

// ownerHint names who manages a resource that carries a label or an
// annotation, see the ownerHints key of the config. kubenorm diff shows the
// hints of resources on one side only.
type ownerHint struct {
	// Label or Annotation is the key to look for; exactly one is set.
	Label      string `yaml:"label"`
	Annotation string `yaml:"annotation"`
	// Value, if set, must equal the value of the key.
	Value *string `yaml:"value"`
	// Text is the hint; "{value}" is replaced with the value of the key.
	// Default: "<key> {value}".
	Text string `yaml:"text"`
}

// maxHintValue limits the length of a value in a hint, so a long
// annotation keeps the "managed by" line readable.
const maxHintValue = 100

// hint returns the hint for a resource with these labels and annotations,
// or false if the hint does not apply.
func (h ownerHint) hint(labels, annotations map[string]string) (string, bool) {
	key, values := h.Label, labels
	if h.Annotation != "" {
		key, values = h.Annotation, annotations
	}

	v, ok := values[key]
	if !ok || (h.Value != nil && v != *h.Value) {
		return "", false
	}

	if len(v) > maxHintValue {
		v = v[:maxHintValue] + "... (truncated)"
	}

	text := h.Text
	if text == "" {
		text = key + " {value}"
	}

	return strings.ReplaceAll(text, "{value}", v), true
}

func validateOwnerHints(name string, hints []ownerHint) error {
	for idx, h := range hints {
		err := validateOwnerHint(h)
		if err != nil {
			return fmt.Errorf("invalid ownerHints[%d] in %s: %w", idx+1, name, err)
		}
	}

	return nil
}

func validateOwnerHint(h ownerHint) error {
	if (h.Label == "") == (h.Annotation == "") {
		return errors.New("set exactly one of label or annotation")
	}

	key, what := h.Label, kindLabel
	if h.Annotation != "" {
		// The api-server checks annotation keys lowercased.
		key, what = strings.ToLower(h.Annotation), kindAnnotation
	}

	if errs := validation.IsQualifiedName(key); len(errs) > 0 {
		return fmt.Errorf("invalid %s key %q: %s", what, key, strings.Join(errs, "; "))
	}

	return nil
}

// OwnerHints returns the hints of the ownerHints rules of the config that
// apply to a resource with these labels and annotations, in the order of
// the config files.
func (c *Config) OwnerHints(labels, annotations map[string]string) []string {
	if c == nil {
		return nil
	}

	var hints []string

	for _, h := range c.ownerHints {
		if text, ok := h.hint(labels, annotations); ok {
			hints = append(hints, text)
		}
	}

	return hints
}
