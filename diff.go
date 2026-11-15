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

// computeDiff compares old and new and returns one unified diff (git apply
// compatible) per differing resource, sorted by path.
func computeDiff(oldSide, newSide normalize.Rendered, labels labels) []string {
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

// sourceDiff holds the differences of one input file.
type sourceDiff struct {
	Source string
	Diffs  []string
}

// diffBySource compares the raw and the normalized version of the same
// resources (raw[i] is the input of normalized[i]) file by file, which
// allows separate trees with the same resources. A raw resource is compared
// under the path of its normalized version, so a changed namespace shows up
// as a change, not as a deleted and an added file. labels returns the diff
// labels for a source file. The result keeps the order of the files.
func diffBySource(raw, normalized []normalize.Resource, labels func(source string) labels) ([]sourceDiff, error) {
	rawBySource, files := groupResourcesBySource(raw)
	normBySource, _ := groupResourcesBySource(normalized)

	result := make([]sourceDiff, len(files))

	for i, f := range files {
		newRendered, err := normalize.Render(normBySource[f])
		if err != nil {
			return nil, err
		}

		oldRendered := make(normalize.Rendered, len(rawBySource[f]))

		for j, r := range rawBySource[f] {
			var buf bytes.Buffer

			err = normalize.Encode(&buf, r.Object)
			if err != nil {
				return nil, err
			}

			oldRendered[normBySource[f][j].RelPath()] = buf.Bytes()
		}

		result[i] = sourceDiff{Source: f, Diffs: computeDiff(oldRendered, newRendered, labels(f))}
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
