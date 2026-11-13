package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverConfigFrom(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	deep := filepath.Join(repo, "a", "b")

	err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	err = os.MkdirAll(deep, 0o755)
	if err != nil {
		t.Fatal(err)
	}

	// Outside of the repository: must not be found.
	writeFile(t, filepath.Join(root, discoveredConfigName), "removeFields: []\n")

	got, err := discoverConfigFrom(deep)
	if err != nil || got != "" {
		t.Fatalf("expected no config inside the repo, got %q %v", got, err)
	}

	writeFile(t, filepath.Join(repo, "a", discoveredConfigName), "removeFields: []\n")

	got, err = discoverConfigFrom(deep)
	if err != nil || got != filepath.Join(repo, "a", discoveredConfigName) {
		t.Fatalf("got %q %v", got, err)
	}
}

// Not parallel: changes the working directory.
func TestNorm_DiscoversConfig(t *testing.T) {
	dir := t.TempDir()

	err := os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(dir, discoveredConfigName), "removeFields:\n  - kind: Deployment\n    fields:\n      - spec.replicas\n")
	t.Chdir(dir)

	_, out, errOut := runCLI(t, deploymentYAML, "norm", "-")
	if strings.Contains(out, "replicas:") {
		t.Fatalf("expected replicas to be removed by %s:\n%s%s", discoveredConfigName, out, errOut)
	}

	_, out, _ = runCLI(t, deploymentYAML, "norm", "-", "--no-config-discovery")
	if !strings.Contains(out, "replicas: 2") {
		t.Fatalf("expected replicas with --no-config-discovery:\n%s", out)
	}
}
