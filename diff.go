package main

import (
	"bytes"
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

// labels returns the "---" and "+++" labels of the diff of a resource.
type labels func(rel string) (oldLabel, newLabel string)

// prefixLabels labels a resource with its path below the given prefixes,
// like git diff does.
func prefixLabels(oldPrefix, newPrefix string) labels {
	return func(rel string) (string, string) {
		return path.Join(oldPrefix, rel), path.Join(newPrefix, rel)
	}
}

// computeDiff compares old and new and returns one entry per differing
// resource, sorted by path: a unified diff for a modified resource. A resource
// that exists on one side only gets one line like "diff -r" prints ("Only in
// new/ns/ConfigMap: b.yaml"), or, with full, a diff from or to /dev/null.
// Only with full is the whole output git apply compatible.
func computeDiff(oldSide, newSide normalize.Rendered, labels labels, full bool) []string {
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

	diffs := make([]string, 0, len(paths))

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

		if !full && status != statusModified {
			diffs = append(diffs, onlyIn(fromLabel, toLabel))

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

		diffs = append(diffs, text)
	}

	return diffs
}

// onlyIn returns the "diff -r" style line of a resource that exists on one
// side only: the label that is not /dev/null.
func onlyIn(fromLabel, toLabel string) string {
	label := toLabel
	if label == devNull {
		label = fromLabel
	}

	return fmt.Sprintf("Only in %s: %s\n", path.Dir(label), path.Base(label))
}

// sourceDiff holds the differences of one input file.
type sourceDiff struct {
	Source string
	Diffs  []string
}

// checkPair is a resource of norm --check: the input as it is and, unless
// normalization drops it, its normalized version. Path is where the
// normalized version goes, so a changed namespace shows up as a change, not
// as a deleted and an added file.
type checkPair struct {
	raw        normalize.Resource
	normalized *normalize.Resource
	path       string
}

// diffBySource compares the pairs file by file, which allows separate trees
// with the same resources. labels returns the diff labels for a source file.
// The result keeps the order of the files.
func diffBySource(pairs []checkPair, labels func(source string) labels) ([]sourceDiff, error) {
	bySource := map[string][]checkPair{}

	var files []string

	for _, p := range pairs {
		if _, ok := bySource[p.raw.Source]; !ok {
			files = append(files, p.raw.Source)
		}

		bySource[p.raw.Source] = append(bySource[p.raw.Source], p)
	}

	result := make([]sourceDiff, len(files))

	for i, f := range files {
		var normalized []normalize.Resource

		oldRendered := normalize.Rendered{}

		for _, p := range bySource[f] {
			var buf bytes.Buffer

			err := normalize.Encode(&buf, p.raw.Object)
			if err != nil {
				return nil, err
			}

			oldRendered[p.path] = buf.Bytes()

			if p.normalized != nil {
				normalized = append(normalized, *p.normalized)
			}
		}

		newRendered, err := normalize.Render(normalized)
		if err != nil {
			return nil, err
		}

		result[i] = sourceDiff{Source: f, Diffs: computeDiff(oldRendered, newRendered, labels(f), true)}
	}

	return result, nil
}
