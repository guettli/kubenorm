package normalize

import (
	"errors"
	"fmt"
	"strings"
)

// setMetadataRuleFile sets labels and annotations on the selected resources,
// see the setMetadata key of the config.
type setMetadataRuleFile struct {
	Group       *string           `yaml:"group"`
	Kind        *string           `yaml:"kind"`
	Namespace   *string           `yaml:"namespace"`
	Name        *string           `yaml:"name"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
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
		m, err := compileMatcher(f.Group, f.Kind, f.Namespace, f.Name)
		if err != nil {
			return nil, fmt.Errorf("invalid setMetadata[%d] in %s: %w", idx+1, name, err)
		}

		if len(f.Labels) == 0 && len(f.Annotations) == 0 {
			return nil, fmt.Errorf("invalid setMetadata[%d] in %s: set labels or annotations", idx+1, name)
		}

		err = validateMetadataValues(f.Labels, f.Annotations)
		if err != nil {
			return nil, fmt.Errorf("invalid setMetadata[%d] in %s: %w", idx+1, name, err)
		}

		rules = append(rules, setMetadataRule{
			matcher:     m,
			labels:      f.Labels,
			annotations: f.Annotations,
			Origin:      fmt.Sprintf("setMetadata[%d] in %s", idx+1, name),
		})
	}

	return rules, nil
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

// OwnerHint names who manages a resource that carries a label or an
// annotation, see the ownerHints key of the config. kubenorm diff shows the
// hints of resources on one side only.
type OwnerHint struct {
	// Label or Annotation is the key to look for; exactly one is set.
	Label      string `yaml:"label"`
	Annotation string `yaml:"annotation"`
	// Value, if set, must equal the value of the key.
	Value string `yaml:"value"`
	// Text is the hint; "{value}" is replaced with the value of the key.
	// Default: "<key> {value}".
	Text string `yaml:"text"`
}

// Hint returns the hint for a resource with these labels and annotations, or
// false if the hint does not apply.
func (h OwnerHint) Hint(labels, annotations map[string]string) (string, bool) {
	key, values := h.Label, labels
	if h.Annotation != "" {
		key, values = h.Annotation, annotations
	}

	v, ok := values[key]
	if !ok || (h.Value != "" && v != h.Value) {
		return "", false
	}

	text := h.Text
	if text == "" {
		text = key + " {value}"
	}

	return strings.ReplaceAll(text, "{value}", v), true
}

func validateOwnerHints(name string, hints []OwnerHint) error {
	for idx, h := range hints {
		if (h.Label == "") == (h.Annotation == "") {
			return fmt.Errorf("invalid ownerHints[%d] in %s: %w", idx+1, name, errors.New("set exactly one of label or annotation"))
		}
	}

	return nil
}

// OwnerHints returns the owner hints of the config, in the order of the
// files.
func (c *Config) OwnerHints() []OwnerHint {
	if c == nil {
		return nil
	}

	return append([]OwnerHint(nil), c.ownerHints...)
}
