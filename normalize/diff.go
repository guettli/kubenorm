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

// Diff writes a unified diff (git style, with a/ and b/ prefixes) from old to
// new. Resources only present on one side are shown as fully added/removed.
func Diff(w io.Writer, oldSide, newSide Rendered, oldPrefix, newPrefix string) (DiffResult, error) {
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

	var result DiffResult

	for _, rel := range paths {
		oldContent, inOld := oldSide[rel]
		newContent, inNew := newSide[rel]

		fromLabel := path.Join(oldPrefix, rel)
		toLabel := path.Join(newPrefix, rel)

		switch {
		case !inOld:
			fromLabel = devNull

			result.Added = append(result.Added, rel)
		case !inNew:
			toLabel = devNull

			result.Removed = append(result.Removed, rel)
		case string(oldContent) == string(newContent):
			continue
		default:
			result.Changed = append(result.Changed, rel)
		}

		edits := myers.ComputeEdits(span.URIFromPath(rel), string(oldContent), string(newContent))
		unified := fmt.Sprint(gotextdiff.ToUnified(fromLabel, toLabel, string(oldContent), edits))

		// gotextdiff writes "-1" / "+1" for an empty side; patch and git
		// apply expect "-0,0" / "+0,0" for created and deleted files.
		switch {
		case !inOld:
			unified = strings.Replace(unified, "\n@@ -1 +", "\n@@ -0,0 +", 1)
		case !inNew:
			unified = strings.Replace(unified, " +1 @@\n", " +0,0 @@\n", 1)
		}

		_, err := io.WriteString(w, unified)
		if err != nil {
			return result, fmt.Errorf("writing diff: %w", err)
		}
	}

	return result, nil
}
