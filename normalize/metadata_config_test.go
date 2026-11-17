package normalize

import (
	"strings"
	"testing"
)

func TestConfig_SetMetadata(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfig("test.yaml", []byte(`
setMetadata:
  - kind: ConfigMap
    labels:
      team: platform
    annotations:
      example.com/note: from config
  - kind: Secret
    labels:
      team: secret
removeFields:
  - fields:
      - metadata.labels.team
`))
	if err != nil {
		t.Fatal(err)
	}

	resources, err := ParseYAML([]byte(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm
  namespace: demo
  labels:
    team: old
`), "test")
	if err != nil {
		t.Fatal(err)
	}

	// The CLI value wins over the config.
	opts := &Options{Config: cfg, SetAnnotations: map[string]string{"example.com/note": "from cli"}}

	explanations, err := opts.Explain(CloneResources(resources))
	if err != nil {
		t.Fatal(err)
	}

	reasons := make([]string, 0, len(explanations[0].Changes))
	for _, c := range explanations[0].Changes {
		reasons = append(reasons, c.Reason)
	}

	if !strings.Contains(strings.Join(reasons, "\n"), "setMetadata[1] in test.yaml: team=platform") {
		t.Errorf("explain misses the setMetadata rule: %q", reasons)
	}

	resources, err = opts.Process(resources)
	if err != nil {
		t.Fatal(err)
	}

	rendered, err := Render(resources)
	if err != nil {
		t.Fatal(err)
	}

	want := `apiVersion: v1
kind: ConfigMap
metadata:
  annotations:
    example.com/note: from cli
  labels:
    team: platform
  name: cm
  namespace: demo
`
	if got := string(rendered["demo/ConfigMap/cm.yaml"]); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestConfig_SetMetadataAndOwnerHintsInvalid(t *testing.T) {
	t.Parallel()

	for config, want := range map[string]string{
		"setMetadata:\n  - kind: ConfigMap\n":               "set labels or annotations",
		"setMetadata:\n  - kind: '['\n    labels: {a: b}\n": "invalid glob",
		"setMetadata:\n  - labels: {a: not valid}\n":        `label "a"`,
		"ownerHints:\n  - text: x\n":                        "exactly one of label or annotation",
		"ownerHints:\n  - label: a\n    annotation: b\n":    "exactly one of label or annotation",
		"ownerHints:\n  - label: a\n    unknown: b\n":       "unknown",
	} {
		_, err := ParseConfig("test.yaml", []byte(config))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: want error with %q, got %v", config, want, err)
		}
	}
}

func TestOwnerHint(t *testing.T) {
	t.Parallel()

	labels := map[string]string{"example.com/team": "web"}
	annotations := map[string]string{"example.com/tool": "pulumi"}

	for _, tc := range []struct {
		hint OwnerHint
		want string
		ok   bool
	}{
		{OwnerHint{Label: "example.com/team", Text: "team {value}"}, "team web", true},
		{OwnerHint{Label: "example.com/team"}, "example.com/team web", true},
		{OwnerHint{Annotation: "example.com/tool", Value: "pulumi", Text: "pulumi"}, "pulumi", true},
		{OwnerHint{Annotation: "example.com/tool", Value: "terraform"}, "", false},
		{OwnerHint{Label: "missing"}, "", false},
	} {
		got, ok := tc.hint.Hint(labels, annotations)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%+v: got %q %v, want %q %v", tc.hint, got, ok, tc.want, tc.ok)
		}
	}

	cfg, err := LoadConfig(false, "")
	if err != nil {
		t.Fatal(err)
	}

	if hints := cfg.OwnerHints(); len(hints) != 0 {
		t.Errorf("expected no hints, got %v", hints)
	}

	if hints := (*Config)(nil).OwnerHints(); hints != nil {
		t.Errorf("expected nil, got %v", hints)
	}
}
