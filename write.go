package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/guettli/kubenorm/normalize"
	"github.com/spf13/cobra"
)

// errScopeFlagsWithWrite is returned when --write is combined with flags that
// would make resources disappear from the rewritten files.
var errScopeFlagsWithWrite = errors.New("--write cannot be combined with --namespaces, --kind, --name-regex, --skip-name-glob, --exclude-namespaces or --skip-owned: resources outside the selection would be lost")

func (f *normFlags) hasScopeFlags() bool {
	return f.namespacesCSV != "" || f.kindsCSV != "" || f.nameRegex != "" ||
		f.skipNameGlob != "" || f.excludeNamespacesCSV != "" || f.skipOwned
}

// runWrite rewrites the input files in place, like gofmt -w. A file is only
// rewritten if normalization changes its content (what --check reports);
// comments and key order alone do not trigger a rewrite. The leading comment
// block of a rewritten file is kept, other comments are lost.
func runWrite(cmd *cobra.Command, flags *normFlags, inputs []string) error {
	if slices.Contains(inputs, normalize.StdinPath) {
		return errors.New("--write cannot read from stdin")
	}

	if flags.hasScopeFlags() {
		return errScopeFlagsWithWrite
	}

	normOpts, err := flags.options()
	if err != nil {
		return err
	}

	rawOpts, err := flags.scopeOptions(nil)
	if err != nil {
		return err
	}

	rawOpts.KeepManagedFields = true

	resources, err := normalize.ReadPaths(inputs, nil)
	if err != nil {
		return err
	}

	// Parse once; Process modifies objects in place, so work on a copy.
	rawResources, err := rawOpts.Process(normalize.CloneResources(resources))
	if err != nil {
		return err
	}

	normResources, err := normOpts.Process(resources)
	if err != nil {
		return err
	}

	// Duplicates are only checked within a file: separate trees (one per
	// cluster, say) legitimately contain the same resource.
	// Which files change is decided like --check does, in parallel.
	sourceDiffs, err := normalize.DiffBySource(rawResources, normResources, func(string) (string, string) { return "", "" })
	if err != nil {
		return err
	}

	rawBySource, files := groupBySource(rawResources)
	normBySource, _ := groupBySource(normResources)

	changedFiles := map[string]bool{}

	for _, sd := range sourceDiffs {
		if len(sd.Diffs) > 0 {
			changedFiles[sd.Source] = true
		}
	}

	for _, file := range files {
		if !changedFiles[file] {
			continue
		}

		changed, err := rewriteFile(file, rawBySource[file], normBySource[file])
		if err != nil {
			return err
		}

		switch {
		case changed && len(normBySource[file]) == 0:
			fmt.Fprintf(cmd.ErrOrStderr(), "Removed: %s\n", file)
		case changed:
			fmt.Fprintf(cmd.ErrOrStderr(), "Rewrote: %s\n", file)
		}
	}

	return nil
}

// groupBySource groups resources by file, keeping the document order.
func groupBySource(resources []normalize.Resource) (map[string][]normalize.Resource, []string) {
	bySource := map[string][]normalize.Resource{}

	var files []string

	for _, r := range resources {
		if _, ok := bySource[r.Source]; !ok {
			files = append(files, r.Source)
		}

		bySource[r.Source] = append(bySource[r.Source], r)
	}

	return bySource, files
}

// rewriteFile writes the normalized documents to file, or removes the file
// if no document is left. The caller decides that the file changes.
func rewriteFile(file string, raw, norm []normalize.Resource) (bool, error) {
	original, err := os.ReadFile(file)
	if err != nil {
		return false, fmt.Errorf("failed to read %s: %w", file, err)
	}

	// Refuse to rewrite files with documents kubenorm does not handle
	// (no metadata, SOPS-encrypted): they would be lost.
	docCount, err := normalize.CountDocuments(original)
	if err != nil {
		return false, fmt.Errorf("failed to parse %s: %w", file, err)
	}

	if docCount != len(raw) {
		return false, fmt.Errorf("cannot rewrite %s: it contains %d document(s) that kubenorm does not normalize (no metadata or SOPS-encrypted)", file, docCount-len(raw))
	}

	info, err := os.Stat(file)
	if err != nil {
		return false, fmt.Errorf("failed to inspect %s: %w", file, err)
	}

	if len(norm) == 0 {
		err = os.Remove(file)
		if err != nil {
			return false, fmt.Errorf("failed to remove %s: %w", file, err)
		}

		return true, nil
	}

	var buf bytes.Buffer

	buf.WriteString(leadingComments(original))

	for i, r := range norm {
		if i > 0 {
			buf.WriteString("---\n")
		}

		err = normalize.Encode(&buf, r.Object)
		if err != nil {
			return false, fmt.Errorf("encoding %s: %w", file, err)
		}
	}

	err = os.WriteFile(file, buf.Bytes(), info.Mode().Perm())
	if err != nil {
		return false, fmt.Errorf("failed to write %s: %w", file, err)
	}

	return true, nil
}

// leadingComments returns the comment lines at the top of a file (blank
// lines between them are kept), up to the first YAML content or "---".
func leadingComments(data []byte) string {
	var (
		out     strings.Builder
		pending strings.Builder
	)

	for line := range strings.SplitAfterSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "#"):
			out.WriteString(pending.String())
			pending.Reset()
			out.WriteString(strings.TrimRight(line, "\n") + "\n")
		case trimmed == "":
			pending.WriteString("\n")
		default:
			return out.String()
		}
	}

	return out.String()
}
