package normalize

import (
	"slices"
	"strings"
	"testing"
)

func TestParseConfig(t *testing.T) {
	t.Parallel()

	cfg, err := ParseConfig("inline", []byte("skipResources:\n- kind: Event\nexcludeNamespaces:\n- kube-*\n"))
	if err != nil {
		t.Fatal(err)
	}

	if !cfg.SkipsEveryResourceOf("", "Event") || cfg.SkipsEveryResourceOf("", "ConfigMap") {
		t.Fatal("SkipsEveryResourceOf: expected Event only")
	}

	if got := cfg.ExcludeNamespaces(); !slices.Equal(got, []string{"kube-*"}) {
		t.Fatalf("ExcludeNamespaces() = %v", got)
	}

	_, err = ParseConfig("inline", []byte("rules: []\n"))
	if err == nil || !strings.Contains(err.Error(), `legacy key "rules"`) {
		t.Fatalf("expected legacy key error, got %v", err)
	}
}
