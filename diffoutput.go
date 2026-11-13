package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/guettli/kubenorm/normalize"
)

const (
	colorReset = "\x1b[0m"
	colorBold  = "\x1b[1m"
	colorRed   = "\x1b[31m"
	colorGreen = "\x1b[32m"
	colorCyan  = "\x1b[36m"

	statBarWidth = 40
)

type diffOutput struct {
	stat       bool
	nameStatus bool
	color      string
}

func (d *diffOutput) validate() error {
	if d.stat && d.nameStatus {
		return errors.New("--stat and --name-status are mutually exclusive")
	}

	switch d.color {
	case "auto", "always", "never":
		return nil
	default:
		return fmt.Errorf("invalid --color %q: use auto, always or never", d.color)
	}
}

// useColor decides about colors. "auto" colors only a terminal and respects
// NO_COLOR (https://no-color.org).
func (d *diffOutput) useColor(w io.Writer) bool {
	switch d.color {
	case "always":
		return true
	case "never":
		return false
	}

	if os.Getenv("NO_COLOR") != "" {
		return false
	}

	f, ok := w.(*os.File)
	if !ok {
		return false
	}

	info, err := f.Stat()

	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (d *diffOutput) write(w io.Writer, diffs []normalize.FileDiff) error {
	var b strings.Builder

	color := d.useColor(w)

	switch {
	case d.nameStatus:
		for _, fd := range diffs {
			fmt.Fprintf(&b, "%c\t%s\n", fd.Status, fd.Path)
		}
	case d.stat:
		writeStat(&b, diffs, color)
	default:
		for _, fd := range diffs {
			if color {
				b.WriteString(colorize(fd.Unified))
			} else {
				b.WriteString(fd.Unified)
			}
		}
	}

	_, err := io.WriteString(w, b.String())
	if err != nil {
		return fmt.Errorf("writing diff: %w", err)
	}

	return nil
}

func colorize(unified string) string {
	var b strings.Builder

	for line := range strings.SplitAfterSeq(unified, "\n") {
		content := strings.TrimSuffix(line, "\n")
		nl := line[len(content):]

		switch {
		case strings.HasPrefix(content, "---"), strings.HasPrefix(content, "+++"):
			b.WriteString(colorBold + content + colorReset + nl)
		case strings.HasPrefix(content, "@@"):
			b.WriteString(colorCyan + content + colorReset + nl)
		case strings.HasPrefix(content, "-"):
			b.WriteString(colorRed + content + colorReset + nl)
		case strings.HasPrefix(content, "+"):
			b.WriteString(colorGreen + content + colorReset + nl)
		default:
			b.WriteString(line)
		}
	}

	return b.String()
}

// writeStat writes a summary like git diff --stat.
func writeStat(b *strings.Builder, diffs []normalize.FileDiff, color bool) {
	if len(diffs) == 0 {
		return
	}

	nameWidth, maxChanges, insertions, deletions := 0, 0, 0, 0

	for _, fd := range diffs {
		nameWidth = max(nameWidth, len(fd.Path))
		maxChanges = max(maxChanges, fd.Insertions+fd.Deletions)
		insertions += fd.Insertions
		deletions += fd.Deletions
	}

	countWidth := len(fmt.Sprint(maxChanges))

	for _, fd := range diffs {
		total := fd.Insertions + fd.Deletions
		plus, minus := fd.Insertions, fd.Deletions

		if maxChanges > statBarWidth {
			plus = scaleBar(fd.Insertions, maxChanges)
			minus = scaleBar(fd.Deletions, maxChanges)
		}

		plusBar, minusBar := strings.Repeat("+", plus), strings.Repeat("-", minus)
		if color {
			plusBar = colorGreen + plusBar + colorReset
			minusBar = colorRed + minusBar + colorReset
		}

		fmt.Fprintf(b, " %-*s | %*d %s%s\n", nameWidth, fd.Path, countWidth, total, plusBar, minusBar)
	}

	fmt.Fprintf(b, " %d %s changed, %d insertion%s(+), %d deletion%s(-)\n",
		len(diffs), plural(len(diffs), "resource", "resources"), insertions, plural(insertions, "", "s"), deletions, plural(deletions, "", "s"))
}

func scaleBar(n, maxChanges int) int {
	if n == 0 {
		return 0
	}

	return max(1, n*statBarWidth/maxChanges)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}

	return many
}
