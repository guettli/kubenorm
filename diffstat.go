package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"

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
			for line := range strings.Lines(d.unified) {
				switch {
				case strings.HasPrefix(line, "+++ "), strings.HasPrefix(line, "--- "):
				case strings.HasPrefix(line, "+"):
					s.insertions++
				case strings.HasPrefix(line, "-"):
					s.deletions++
				}
			}
		}

		stats[i] = s
	}

	return stats
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
		width = max(width, len(s.rel))
		most = max(most, s.insertions+s.deletions)
		counts[s.status]++
		insertions += s.insertions
		deletions += s.deletions
	}

	var b strings.Builder

	for _, s := range stats {
		plus, minus := statBar(s.insertions, s.deletions, most)
		fmt.Fprintf(&b, " %c %-*s | %*d %s%s\n", s.status, width, s.rel, len(fmt.Sprint(most)), s.insertions+s.deletions,
			strings.Repeat("+", plus), strings.Repeat("-", minus))
	}

	fmt.Fprintf(&b, " %d %s changed (%d added, %d deleted, %d modified), %d insertions(+), %d deletions(-)\n",
		len(stats), plural(len(stats), "resource"), counts[statusAdded], counts[statusDeleted], counts[statusModified],
		insertions, deletions)

	_, err := io.WriteString(w, b.String())
	if err != nil {
		return fmt.Errorf("writing diff stat: %w", err)
	}

	return nil
}

// statBar returns the number of "+" and "-" of the bar, scaled so that the
// largest change fills maxStatBar. A non-zero count gets at least one sign.
func statBar(insertions, deletions, most int) (int, int) {
	if most <= maxStatBar {
		return insertions, deletions
	}

	scale := func(n int) int {
		if n == 0 {
			return 0
		}

		return max(1, n*maxStatBar/most)
	}

	return scale(insertions), scale(deletions)
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}

	return word + "s"
}
