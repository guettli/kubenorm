package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/guettli/kubenorm/normalize"
)

// outputStat is the summary output of kubenorm diff, like git diff --stat.
const outputStat = "stat"

// maxStatBar is the maximal width of the +/- bar of --output stat.
const maxStatBar = 40

// diffStat counts the changed lines of one resource.
type diffStat struct {
	rel                   string
	status                fileStatus
	insertions, deletions int
}

// countLines returns the number of lines of a rendered resource.
func countLines(content []byte) int {
	n := bytes.Count(content, []byte("\n"))
	if len(content) > 0 && content[len(content)-1] != '\n' {
		n++
	}

	return n
}

// computeStats counts the changed lines of the differences. The diff of a
// modified resource is always computed by compareRendered.
func computeStats(diffs []resourceDiff, oldSide, newSide normalize.Rendered) []diffStat {
	stats := make([]diffStat, len(diffs))

	for i, d := range diffs {
		s := diffStat{rel: d.rel, status: d.status}

		switch d.status {
		case statusAdded:
			s.insertions = countLines(newSide[d.rel])
		case statusDeleted:
			s.deletions = countLines(oldSide[d.rel])
		case statusModified:
			s.insertions, s.deletions = countChanges(d.unified)
		}

		stats[i] = s
	}

	return stats
}

// countChanges counts the added and removed lines of a unified diff. The
// first two lines are the ---/+++ file headers; content lines may start
// with "--" or "++" too.
func countChanges(unified string) (int, int) {
	var insertions, deletions int

	n := 0

	for line := range strings.Lines(unified) {
		n++
		if n <= 2 {
			continue
		}

		switch line[0] {
		case '+':
			insertions++
		case '-':
			deletions++
		}
	}

	return insertions, deletions
}

// writeDiffStat writes one line per differing resource with its status
// (A added, D deleted, M modified), path, number of changed lines and a
// +/- bar, followed by a summary line.
func writeDiffStat(w io.Writer, stats []diffStat) error {
	if len(stats) == 0 {
		return nil
	}

	width, most := 0, 0
	counts := map[fileStatus]int{}

	var insertions, deletions int

	for _, s := range stats {
		width = max(width, utf8.RuneCountInString(s.rel))
		most = max(most, s.insertions+s.deletions)
		counts[s.status]++
		insertions += s.insertions
		deletions += s.deletions
	}

	var b strings.Builder

	for _, s := range stats {
		plus, minus := statBar(s.insertions, s.deletions, most)
		pad := strings.Repeat(" ", width-utf8.RuneCountInString(s.rel))
		fmt.Fprintf(&b, " %c %s%s | %*d %s%s\n", s.status, s.rel, pad, len(fmt.Sprint(most)), s.insertions+s.deletions,
			strings.Repeat("+", plus), strings.Repeat("-", minus))
	}

	b.WriteString(statSummary(len(stats), counts, insertions, deletions))

	_, err := io.WriteString(w, b.String())
	if err != nil {
		return fmt.Errorf("writing diff stat: %w", err)
	}

	return nil
}

// statSummary returns the total line, like git: parts that are zero are
// left out.
func statSummary(resources int, counts map[fileStatus]int, insertions, deletions int) string {
	var kinds []string

	for _, k := range []struct {
		status fileStatus
		name   string
	}{{statusAdded, "added"}, {statusDeleted, "deleted"}, {statusModified, "modified"}} {
		if counts[k.status] > 0 {
			kinds = append(kinds, fmt.Sprintf("%d %s", counts[k.status], k.name))
		}
	}

	out := fmt.Sprintf(" %d %s changed (%s)", resources, plural(resources, "resource"), strings.Join(kinds, ", "))

	if insertions > 0 {
		out += fmt.Sprintf(", %d %s(+)", insertions, plural(insertions, "insertion"))
	}

	if deletions > 0 {
		out += fmt.Sprintf(", %d %s(-)", deletions, plural(deletions, "deletion"))
	}

	return out + "\n"
}

// statBar returns the number of "+" and "-" of the bar. Like git, the
// total is scaled so that the largest change fills maxStatBar, and then
// split; a non-zero count gets at least one sign.
func statBar(insertions, deletions, most int) (int, int) {
	if most <= maxStatBar {
		return insertions, deletions
	}

	scale := func(n int) int {
		if n == 0 {
			return 0
		}

		return 1 + n*(maxStatBar-1)/most
	}

	total := scale(insertions + deletions)
	plus := scale(insertions)

	return plus, total - plus
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}

	return word + "s"
}
