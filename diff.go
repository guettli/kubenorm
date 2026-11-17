package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"path/filepath"
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

// resourceDiff is the difference of one resource.
type resourceDiff struct {
	rel    string
	status fileStatus
	// unified is the unified diff. Empty for a resource on one side only,
	// unless compareRendered was called with full.
	unified string
}

// compareRendered compares old and new and returns one entry per differing
// resource, sorted by path. full adds the unified diff of resources on one
// side only, from or to /dev/null.
func compareRendered(oldSide, newSide normalize.Rendered, labels labels, full bool) []resourceDiff {
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

	diffs := make([]resourceDiff, 0, len(paths))

	for _, rel := range paths {
		oldContent, inOld := oldSide[rel]
		newContent, inNew := newSide[rel]

		status := statusModified

		switch {
		case !inOld:
			status = statusAdded
		case !inNew:
			status = statusDeleted
		case string(oldContent) == string(newContent):
			continue
		}

		d := resourceDiff{rel: rel, status: status}
		if full || status == statusModified {
			d.unified = unifiedDiff(rel, status, oldContent, newContent, labels)
		}

		diffs = append(diffs, d)
	}

	return diffs
}

// unifiedDiff returns the unified diff (git apply compatible) of a resource.
func unifiedDiff(rel string, status fileStatus, oldContent, newContent []byte, labels labels) string {
	fromLabel, toLabel := labels(rel)

	switch status {
	case statusAdded:
		fromLabel = devNull
	case statusDeleted:
		toLabel = devNull
	case statusModified:
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

	return text
}

// computeDiff returns the unified diffs of compareRendered with full, for
// norm --check.
func computeDiff(oldSide, newSide normalize.Rendered, labels labels) []string {
	diffs := compareRendered(oldSide, newSide, labels, true)
	out := make([]string, len(diffs))

	for i, d := range diffs {
		out[i] = d.unified
	}

	return out
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

		result[i] = sourceDiff{Source: f, Diffs: computeDiff(oldRendered, newRendered, labels(f))}
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
	shared bool
	// stdin is true if the resource was read from stdin.
	stdin    bool
	managers []string
}

// location names the resource for a reader: the source file, plus the
// normalized path if the file holds several resources.
func (o origin) location() string {
	source := filepath.ToSlash(o.source)
	if o.shared {
		return source + " (" + o.rel + ")"
	}

	return source
}

// path names the resource in a diff header: the source file, or, if the
// file holds several resources, the normalized path below it. It uses "/"
// on every OS, like patch and git apply expect.
func (o origin) path() string {
	source := path.Clean(filepath.ToSlash(o.source))
	if o.shared {
		return path.Join(source, o.rel)
	}

	return source
}

// originLabels labels a resource with the files it was read from. A label of
// a side without the resource is empty; unifiedDiff replaces it with
// /dev/null.
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
			stdin:    infos[i].source == normalize.StdinPath,
			managers: infos[i].managers,
		}
	}

	return diffSide{rendered: rendered, origins: origins}, nil
}

// onlyHereLine returns the line of a resource that exists on one side
// only; side is "old" or "new".
func onlyHereLine(o origin, side string) string {
	line := "Only here (" + side + "): " + o.location()
	if len(o.managers) > 0 {
		line += " managed by: " + strings.Join(o.managers, ", ")
	}

	return line + "\n"
}

// Output formats of kubenorm diff.
const (
	outputText = "text"
	outputJSON = "json"
)

// writeDiffText writes the unified diffs and, for resources on one side
// only without a diff, their "Only here" line.
func writeDiffText(w io.Writer, diffs []resourceDiff, oldSide, newSide diffSide) error {
	for _, d := range diffs {
		text := d.unified

		switch {
		case text != "":
		case d.status == statusDeleted:
			text = onlyHereLine(oldSide.origins[d.rel], "old")
		default:
			text = onlyHereLine(newSide.origins[d.rel], "new")
		}

		_, err := io.WriteString(w, text)
		if err != nil {
			return fmt.Errorf("writing diff: %w", err)
		}
	}

	return nil
}

// jsonVersion is the version of the JSON output. Fields are only added
// within a version.
const jsonVersion = 1

// jsonDiff is the output of kubenorm diff --output json.
type jsonDiff struct {
	Version   int            `json:"version"`
	Resources []jsonResource `json:"resources"`
}

// jsonResource is one differing resource.
type jsonResource struct {
	// Path is the normalized path, the identity of the resource.
	Path string `json:"path"`
	// Status is added, deleted or modified.
	Status string      `json:"status"`
	Old    *jsonOrigin `json:"old,omitempty"`
	New    *jsonOrigin `json:"new,omitempty"`
	// Diff is the unified diff; for added and deleted resources only with
	// --full.
	Diff string `json:"diff,omitempty"`
}

// jsonOrigin tells where one side of a resource comes from.
type jsonOrigin struct {
	// File is the file the resource was read from, "-" for stdin.
	File string `json:"file"`
	// ManagedBy names the tools that seem to manage the resource, like the
	// text output. The values are for humans and may change.
	ManagedBy []string `json:"managedBy"`
}

// String returns the status name of the JSON output.
func (s fileStatus) String() string {
	switch s {
	case statusAdded:
		return "added"
	case statusDeleted:
		return "deleted"
	case statusModified:
		return "modified"
	}

	return "unknown"
}

// writeDiffJSON writes the differences as one JSON object.
func writeDiffJSON(w io.Writer, diffs []resourceDiff, oldSide, newSide diffSide) error {
	out := jsonDiff{Version: jsonVersion, Resources: make([]jsonResource, len(diffs))}

	side := func(s diffSide, rel string) *jsonOrigin {
		o, ok := s.origins[rel]
		if !ok {
			return nil
		}

		file := filepath.ToSlash(o.source)
		if o.stdin {
			file = normalize.StdinPath
		}

		return &jsonOrigin{File: file, ManagedBy: append([]string{}, o.managers...)}
	}

	for i, d := range diffs {
		out.Resources[i] = jsonResource{
			Path:   d.rel,
			Status: d.status.String(),
			Old:    side(oldSide, d.rel),
			New:    side(newSide, d.rel),
			Diff:   d.unified,
		}
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	err := enc.Encode(out)
	if err != nil {
		return fmt.Errorf("writing diff: %w", err)
	}

	return nil
}
