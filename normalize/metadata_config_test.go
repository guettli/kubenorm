package normalize

import (
	"os"
	"path/filepath"
	"slices"
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

func TestOwnerHints(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfig("test.yaml", []byte(`
ownerHints:
  - label: example.com/team
    text: team {value}
  - label: example.com/team
  - annotation: example.com/tool
    value: pulumi
    text: pulumi
  - annotation: example.com/tool
    value: terraform
  - annotation: example.com/empty
    value: ''
    text: empty
  - annotation: example.com/long
  - label: missing
`))
	if err != nil {
		t.Fatal(err)
	}

	labels := map[string]string{"example.com/team": "web"}
	annotations := map[string]string{
		"example.com/tool":  "pulumi",
		"example.com/empty": "",
		"example.com/long":  strings.Repeat("x", 200),
	}

	want := []string{
		"team web", "example.com/team web", "pulumi", "empty",
		"example.com/long " + strings.Repeat("x", maxHintValue) + "... (truncated)",
	}
	if got := cfg.OwnerHints(labels, annotations); !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}

	// An explicit empty value matches only an empty value.
	if got := cfg.OwnerHints(nil, map[string]string{"example.com/empty": "x"}); len(got) != 0 {
		t.Errorf("got %q, want no hints", got)
	}

	if hints := (*Config)(nil).OwnerHints(labels, annotations); hints != nil {
		t.Errorf("expected nil, got %v", hints)
	}
}

func TestConfig_SetMetadataMergeOrderAndNamespace(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first := filepath.Join(dir, "first.yaml")
	second := filepath.Join(dir, "second.yaml")

	err := os.WriteFile(first, []byte("setMetadata:\n  - namespace: moved\n    labels: {team: first}\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(second, []byte("setMetadata:\n  - namespace: moved\n    labels: {team: second}\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(false, first, second)
	if err != nil {
		t.Fatal(err)
	}

	resources, err := ParseYAML([]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm\n  namespace: old\n"), "test")
	if err != nil {
		t.Fatal(err)
	}

	// The rule matches the namespace after --overwrite-namespace; the later
	// file wins.
	resources, err = (&Options{Config: cfg, OverwriteNamespace: "moved"}).Process(resources)
	if err != nil {
		t.Fatal(err)
	}

	labels := resources[0].Unstructured().GetLabels()
	if labels["team"] != "second" {
		t.Fatalf("got labels %v", labels)
	}
}
