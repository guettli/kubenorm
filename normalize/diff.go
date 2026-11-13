package normalize

import (
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
)

const devNull = "/dev/null"

// FileStatus tells how a resource differs.
type FileStatus byte

const (
	// StatusAdded means the resource only exists on the new side.
	StatusAdded FileStatus = 'A'
	// StatusModified means the resource differs.
	StatusModified FileStatus = 'M'
	// StatusDeleted means the resource only exists on the old side.
	StatusDeleted FileStatus = 'D'
)

// FileDiff is the difference of one resource.
type FileDiff struct {
	// Path is the relative path, see RelPath.
	Path   string
	Status FileStatus
	// Unified is the unified diff (git apply compatible).
	Unified    string
	Insertions int
	Deletions  int
}

// DiffResult summarizes a Diff run.
type DiffResult struct {
	Changed []string
	Added   []string
	Removed []string
}

// Equal reports whether no difference was found.
func (d DiffResult) Equal() bool {
	return len(d.Changed) == 0 && len(d.Added) == 0 && len(d.Removed) == 0
}

// ComputeDiff compares old and new, sorted by path. oldPrefix and newPrefix
// are prepended to the paths in the unified diff headers.
func ComputeDiff(oldSide, newSide Rendered, oldPrefix, newPrefix string) []FileDiff {
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

		fromLabel := path.Join(oldPrefix, rel)
		toLabel := path.Join(newPrefix, rel)
		status := StatusModified

		switch {
		case !inOld:
			fromLabel = devNull
			status = StatusAdded
		case !inNew:
			toLabel = devNull
			status = StatusDeleted
		case string(oldContent) == string(newContent):
			continue
		}

		edits := myers.ComputeEdits(span.URIFromPath(rel), string(oldContent), string(newContent))
		unified := gotextdiff.ToUnified(fromLabel, toLabel, string(oldContent), edits)
		text := fmt.Sprint(unified)

		// gotextdiff writes "-1" / "+1" for an empty side; patch and git
		// apply expect "-0,0" / "+0,0" for created and deleted files.
		switch status {
		case StatusAdded:
			text = strings.Replace(text, "\n@@ -1 +", "\n@@ -0,0 +", 1)
		case StatusDeleted:
			text = strings.Replace(text, " +1 @@\n", " +0,0 @@\n", 1)
		case StatusModified:
		}

		d := FileDiff{Path: rel, Status: status, Unified: text}

		for _, hunk := range unified.Hunks {
			for _, line := range hunk.Lines {
				switch line.Kind {
				case gotextdiff.Insert:
					d.Insertions++
				case gotextdiff.Delete:
					d.Deletions++
				case gotextdiff.Equal:
				}
			}
		}

		diffs = append(diffs, d)
	}

	return diffs
}

// Summarize returns the paths per status.
func Summarize(diffs []FileDiff) DiffResult {
	var result DiffResult

	for _, d := range diffs {
		switch d.Status {
		case StatusAdded:
			result.Added = append(result.Added, d.Path)
		case StatusDeleted:
			result.Removed = append(result.Removed, d.Path)
		case StatusModified:
			result.Changed = append(result.Changed, d.Path)
		}
	}

	return result
}

// Diff writes a unified diff (git style) from old to new. Resources only
// present on one side are shown as fully added/removed.
func Diff(w io.Writer, oldSide, newSide Rendered, oldPrefix, newPrefix string) (DiffResult, error) {
	diffs := ComputeDiff(oldSide, newSide, oldPrefix, newPrefix)

	for _, d := range diffs {
		_, err := io.WriteString(w, d.Unified)
		if err != nil {
			return DiffResult{}, fmt.Errorf("writing diff: %w", err)
		}
	}

	return Summarize(diffs), nil
}

// SourceDiff holds the differences of one input file.
type SourceDiff struct {
	Source string
	Diffs  []FileDiff
}

// DiffBySource compares two processed versions of the same input file by
// file (for example raw and normalized). Comparing per file allows separate
// trees with the same resources. prefixes returns the diff path prefixes for
// a source file. Files are compared in parallel, the result keeps the order
// in which the files first appear in oldSide.
func DiffBySource(oldSide, newSide []Resource, prefixes func(source string) (string, string)) ([]SourceDiff, error) {
	oldBySource, files := groupResourcesBySource(oldSide)
	newBySource, newFiles := groupResourcesBySource(newSide)

	for _, f := range newFiles {
		if _, ok := oldBySource[f]; !ok {
			files = append(files, f)
		}
	}

	result := make([]SourceDiff, len(files))

	err := parallelFor(len(files), func(i int) error {
		oldRendered, err := Render(oldBySource[files[i]])
		if err != nil {
			return err
		}

		newRendered, err := Render(newBySource[files[i]])
		if err != nil {
			return err
		}

		oldPrefix, newPrefix := prefixes(files[i])
		result[i] = SourceDiff{Source: files[i], Diffs: ComputeDiff(oldRendered, newRendered, oldPrefix, newPrefix)}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// groupResourcesBySource groups resources by file, keeping the order.
func groupResourcesBySource(resources []Resource) (map[string][]Resource, []string) {
	bySource := map[string][]Resource{}

	var files []string

	for _, r := range resources {
		if _, ok := bySource[r.Source]; !ok {
			files = append(files, r.Source)
		}

		bySource[r.Source] = append(bySource[r.Source], r)
	}

	return bySource, files
}
