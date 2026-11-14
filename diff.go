package main

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/guettli/kubenorm/normalize"
	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
)

const devNull = "/dev/null"

// fileStatus tells how a resource differs.
type fileStatus byte

const (
	statusAdded    fileStatus = 'A'
	statusModified fileStatus = 'M'
	statusDeleted  fileStatus = 'D'
)

// FileDiff is the difference of one resource.
type FileDiff struct {
	// Path is the relative path, see RelPath.
	Path   string
	Status fileStatus
	// Unified is the unified diff (git apply compatible).
	Unified string
}

// Labels returns the "---" and "+++" labels of the diff of a resource.
type Labels func(rel string) (oldLabel, newLabel string)

// PrefixLabels labels a resource with its path below the given prefixes,
// like git diff does.
func PrefixLabels(oldPrefix, newPrefix string) Labels {
	return func(rel string) (string, string) {
		return path.Join(oldPrefix, rel), path.Join(newPrefix, rel)
	}
}

// ComputeDiff compares old and new, sorted by path.
func ComputeDiff(oldSide, newSide normalize.Rendered, labels Labels) []FileDiff {
	all := make(map[string]struct{}, len(oldSide)+len(newSide))
	for p := range oldSide {
		all[p] = struct{}{}
	}

	for p := range newSide {
		all[p] = struct{}{}
	}

	paths := make([]string, 0, len(all))
	for p := range all {
		paths = append(paths, p)
	}

	sort.Strings(paths)

	diffs := make([]FileDiff, 0, len(paths))

	for _, rel := range paths {
		oldContent, inOld := oldSide[rel]
		newContent, inNew := newSide[rel]

		fromLabel, toLabel := labels(rel)
		status := statusModified

		switch {
		case !inOld:
			fromLabel = devNull
			status = statusAdded
		case !inNew:
			toLabel = devNull
			status = statusDeleted
		case string(oldContent) == string(newContent):
			continue
		}

		edits := myers.ComputeEdits(span.URIFromPath(rel), string(oldContent), string(newContent))
		text := fmt.Sprint(gotextdiff.ToUnified(fromLabel, toLabel, string(oldContent), edits))

		// gotextdiff writes "-1" / "+1" for an empty side; patch and git
		// apply expect "-0,0" / "+0,0" for created and deleted files.
		switch status {
		case statusAdded:
			text = strings.Replace(text, "\n@@ -1 +", "\n@@ -0,0 +", 1)
		case statusDeleted:
			text = strings.Replace(text, " +1 @@\n", " +0,0 @@\n", 1)
		case statusModified:
		}

		diffs = append(diffs, FileDiff{Path: rel, Status: status, Unified: text})
	}

	return diffs
}

// SourceDiff holds the differences of one input file.
type SourceDiff struct {
	Source string
	Diffs  []FileDiff
}

// DiffBySource compares two processed versions of the same input file by
// file (for example raw and normalized). Comparing per file allows separate
// trees with the same resources. labels returns the diff labels for a
// source file. The result keeps the order in which the files first appear
// in oldSide.
func DiffBySource(oldSide, newSide []normalize.Resource, labels func(source string) Labels) ([]SourceDiff, error) {
	oldBySource, files := groupResourcesBySource(oldSide)
	newBySource, newFiles := groupResourcesBySource(newSide)

	for _, f := range newFiles {
		if _, ok := oldBySource[f]; !ok {
			files = append(files, f)
		}
	}

	result := make([]SourceDiff, len(files))

	for i, f := range files {
		oldRendered, err := normalize.Render(oldBySource[f])
		if err != nil {
			return nil, err
		}

		newRendered, err := normalize.Render(newBySource[f])
		if err != nil {
			return nil, err
		}

		result[i] = SourceDiff{Source: f, Diffs: ComputeDiff(oldRendered, newRendered, labels(f))}
	}

	return result, nil
}

// groupResourcesBySource groups resources by file, keeping the order.
func groupResourcesBySource(resources []normalize.Resource) (map[string][]normalize.Resource, []string) {
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
