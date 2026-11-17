package main

import (
	"bytes"
	"fmt"
	"path"
	"reflect"
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
// that exists on one side only gets the line onlyHere returns for it, or, if
// onlyHere is nil, a diff from or to /dev/null. Only with a nil onlyHere is
// the whole output git apply compatible.
func computeDiff(oldSide, newSide normalize.Rendered, labels labels, onlyHere func(rel string, inOld bool) string) []string {
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

		if onlyHere != nil && status != statusModified {
			diffs = append(diffs, onlyHere(rel, inOld))

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

		result[i] = sourceDiff{Source: f, Diffs: computeDiff(oldRendered, newRendered, labels(f), nil)}
	}

	return result, nil
}

// diffSide is one side of kubenorm diff: the rendered resources and, by
// path, where each one comes from.
type diffSide struct {
	rendered normalize.Rendered
	origins  map[string]origin
}

// origin tells where a resource was read from and who manages it.
type origin struct {
	// source is the file the resource was read from, "stdin" for stdin.
	source string
	// rel is the normalized path of the resource.
	rel string
	// shared is true if the source holds other resources too (or is stdin),
	// so the source alone does not name the resource.
	shared   bool
	managers []string
}

// location names the resource for a reader: the source file, plus the
// normalized path if the file holds several resources.
func (o origin) location() string {
	if o.shared {
		return o.source + " (" + o.rel + ")"
	}

	return o.source
}

// path names the resource in a diff header: the source file, or, if the
// file holds several resources, the normalized path below it, like before.
func (o origin) path() string {
	if o.shared {
		return path.Join(o.source, o.rel)
	}

	return o.source
}

// originLabels labels a resource with the files it was read from, so a
// diff header names real files. Before, it was the input argument joined
// with the normalized path, which duplicated the namespace for an argument
// like zz_rendered/ns1.
func originLabels(oldSide, newSide diffSide) labels {
	return func(rel string) (string, string) {
		return oldSide.origins[rel].path(), newSide.origins[rel].path()
	}
}

// rawInfo is what newDiffSide reads from a resource before processing.
type rawInfo struct {
	source   string
	managers []string
}

// newDiffSide processes and renders the resources. Who manages a resource is
// read before normalization removes the fields that tell it.
func newDiffSide(resources []normalize.Resource, opts *normalize.Options) (diffSide, error) {
	// Process normalizes the objects in place, so their maps identify them.
	raw := make(map[uintptr]rawInfo, len(resources))
	for _, r := range resources {
		raw[reflect.ValueOf(r.Object).Pointer()] = rawInfo{source: r.Source, managers: managers(r.Object)}
	}

	kept, err := opts.Process(resources)
	if err != nil {
		return diffSide{}, err
	}

	rendered, err := normalize.Render(kept)
	if err != nil {
		return diffSide{}, err
	}

	infos := make([]rawInfo, len(kept))
	perSource := map[string]int{}

	for i := range kept {
		info, ok := raw[reflect.ValueOf(kept[i].Object).Pointer()]
		if !ok {
			info.source = kept[i].Source
		}

		infos[i] = info
		perSource[info.source]++
	}

	origins := make(map[string]origin, len(kept))

	for i := range kept {
		rel := kept[i].RelPath()

		origins[rel] = origin{
			source:   diffLabel(infos[i].source),
			rel:      rel,
			shared:   infos[i].source == normalize.StdinPath || perSource[infos[i].source] > 1,
			managers: infos[i].managers,
		}
	}

	return diffSide{rendered: rendered, origins: origins}, nil
}

// onlyHereLine returns the line of a resource that exists on one side only.
func onlyHereLine(o origin) string {
	line := "Only here: " + o.location()
	if len(o.managers) > 0 {
		line += " managed by: " + strings.Join(o.managers, ", ")
	}

	return line + "\n"
}
