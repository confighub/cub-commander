package exec

import (
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// DiffLine is one line of a unified diff: kind is ' ', '+', '-' or '@'.
type DiffLine struct {
	Kind byte
	Text string
}

// Unified computes a line diff of a against b with context lines around each
// change, the way `diff -u` reads.
func Unified(a, b string, context int) []DiffLine {
	dmp := diffmatchpatch.New()
	ca, cb, lines := dmp.DiffLinesToChars(a, b)
	diffs := dmp.DiffCharsToLines(dmp.DiffMain(ca, cb, false), lines)
	var all []DiffLine
	for _, d := range diffs {
		kind := byte(' ')
		switch d.Type {
		case diffmatchpatch.DiffInsert:
			kind = '+'
		case diffmatchpatch.DiffDelete:
			kind = '-'
		}
		for _, l := range strings.Split(strings.TrimSuffix(d.Text, "\n"), "\n") {
			all = append(all, DiffLine{Kind: kind, Text: l})
		}
	}
	// Keep only changes and their context.
	keep := make([]bool, len(all))
	for i, l := range all {
		if l.Kind != ' ' {
			for j := max(0, i-context); j <= min(len(all)-1, i+context); j++ {
				keep[j] = true
			}
		}
	}
	var out []DiffLine
	prevKept := true
	for i, l := range all {
		if !keep[i] {
			prevKept = false
			continue
		}
		if !prevKept {
			out = append(out, DiffLine{Kind: '@', Text: "…"})
		}
		out = append(out, l)
		prevKept = true
	}
	return out
}

// Changed counts inserted and deleted lines.
func Changed(lines []DiffLine) (plus, minus int) {
	for _, l := range lines {
		switch l.Kind {
		case '+':
			plus++
		case '-':
			minus++
		}
	}
	return
}
