package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const discoveredConfigName = ".kubenorm.yaml"

// discoverConfig looks for .kubenorm.yaml in the current directory and its
// parents. The search stops at the root of the git repository (a directory
// containing .git) or the file system root. It returns "" if none is found.
func discoverConfig() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getting working directory: %w", err)
	}

	return discoverConfigFrom(dir)
}

func discoverConfigFrom(dir string) (string, error) {
	for {
		candidate := filepath.Join(dir, discoveredConfigName)

		_, err := os.Stat(candidate)
		if err == nil {
			return candidate, nil
		}

		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspecting %s: %w", candidate, err)
		}

		_, err = os.Stat(filepath.Join(dir, ".git"))
		if err == nil {
			return "", nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}

		dir = parent
	}
}
