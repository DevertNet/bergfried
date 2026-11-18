package skills

import (
	"fmt"
	"strings"
)

// diffOp is one line of an edit script.
type diffOp struct {
	kind byte // ' ' unchanged, '-' removed, '+' added
	text string
}

// unifiedDiff returns a unified-style diff of oldText and newText with three
// lines of context and "@@ -a,b +c,d @@" hunk headers. Empty text produces no
// lines, so a create shows only '+' lines and a delete only '-' lines.
// A missing final newline is not marked separately.
func unifiedDiff(oldText, newText string) string {
	return formatDiff(diffOps(splitLines(oldText), splitLines(newText)))
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffOps computes an LCS-based edit script. Very large inputs fall back to
// "everything removed, everything added" to keep the memory bounded.
func diffOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	const maxCells = 1_000_000
	if int64(n)*int64(m) > maxCells {
		ops := make([]diffOp, 0, n+m)
		for _, s := range a {
			ops = append(ops, diffOp{'-', s})
		}
		for _, s := range b {
			ops = append(ops, diffOp{'+', s})
		}
		return ops
	}

	w := m + 1
	// dp[i*w+j] = LCS length of a[i:] and b[j:].
	dp := make([]int32, (n+1)*w)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				dp[i*w+j] = dp[(i+1)*w+j+1] + 1
			case dp[(i+1)*w+j] >= dp[i*w+j+1]:
				dp[i*w+j] = dp[(i+1)*w+j]
			default:
				dp[i*w+j] = dp[i*w+j+1]
			}
		}
	}

	ops := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		case dp[(i+1)*w+j] >= dp[i*w+j+1]:
			ops = append(ops, diffOp{'-', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', b[j]})
	}
	return ops
}

// formatDiff groups the edit script into hunks with context lines.
func formatDiff(ops []diffOp) string {
	const ctx = 3
	changes := make([]int, 0)
	for i, o := range ops {
		if o.kind != ' ' {
			changes = append(changes, i)
		}
	}
	if len(changes) == 0 {
		return ""
	}

	// oldBefore[i]/newBefore[i] count the consumed lines before op i.
	oldBefore := make([]int, len(ops)+1)
	newBefore := make([]int, len(ops)+1)
	for i, o := range ops {
		oldBefore[i+1] = oldBefore[i]
		newBefore[i+1] = newBefore[i]
		if o.kind != '+' {
			oldBefore[i+1]++
		}
		if o.kind != '-' {
			newBefore[i+1]++
		}
	}

	var b strings.Builder
	for i := 0; i < len(changes); {
		last := changes[i]
		j := i + 1
		for j < len(changes) && changes[j]-last-1 <= 2*ctx {
			last = changes[j]
			j++
		}
		start := changes[i] - ctx
		if start < 0 {
			start = 0
		}
		end := last + ctx + 1
		if end > len(ops) {
			end = len(ops)
		}
		oldStart, newStart := oldBefore[start]+1, newBefore[start]+1
		oldCount := oldBefore[end] - oldBefore[start]
		newCount := newBefore[end] - newBefore[start]
		if oldCount == 0 {
			oldStart = oldBefore[start]
		}
		if newCount == 0 {
			newStart = newBefore[start]
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, o := range ops[start:end] {
			b.WriteByte(o.kind)
			b.WriteString(o.text)
			b.WriteByte('\n')
		}
		i = j
	}
	return b.String()
}
