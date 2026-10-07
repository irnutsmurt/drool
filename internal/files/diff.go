package files

import (
	"fmt"
	"strings"
)

// MaxDiffLines bounds the inputs Diff works on; beyond it callers report
// that no diff is available rather than spending unbounded CPU.
const MaxDiffLines = 20000

// Diff returns a unified diff (3 lines of context) between two texts, or ""
// when they are equal. ok is false when either side is too large.
func Diff(oldName, newName, a, b string) (diff string, ok bool) {
	if a == b {
		return "", true
	}
	x, y := splitLines(a), splitLines(b)
	if len(x) > MaxDiffLines || len(y) > MaxDiffLines {
		return "", false
	}
	ops := myers(x, y)
	return unified(oldName, newName, x, y, ops), true
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type op struct {
	kind byte // ' ', '-', '+'
	ai   int  // index in a (for ' ' and '-')
	bi   int  // index in b (for ' ' and '+')
}

// myers computes a shortest edit script (Myers' O(ND) algorithm).
func myers(a, b []string) []op {
	n, m := len(a), len(b)
	max := n + m
	v := make([]int, 2*max+2)
	var trace [][]int
	for d := 0; d <= max; d++ {
		trace = append(trace, append([]int(nil), v...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[max+k-1] < v[max+k+1]) {
				x = v[max+k+1]
			} else {
				x = v[max+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[max+k] = x
			if x >= n && y >= m {
				return backtrack(trace, a, b, max, d)
			}
		}
	}
	return nil
}

func backtrack(trace [][]int, a, b []string, max, d int) []op {
	x, y := len(a), len(b)
	var ops []op
	for ; d > 0; d-- {
		v := trace[d]
		k := x - y
		var pk int
		if k == -d || (k != d && v[max+k-1] < v[max+k+1]) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := v[max+pk]
		py := px - pk
		for x > px && y > py {
			x--
			y--
			ops = append(ops, op{' ', x, y})
		}
		if x == px {
			y--
			ops = append(ops, op{'+', x, y})
		} else {
			x--
			ops = append(ops, op{'-', x, y})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		ops = append(ops, op{' ', x, y})
	}
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops
}

func unified(oldName, newName string, a, b []string, ops []op) string {
	const ctx = 3
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", oldName, newName)
	line := func(prefix byte, s string) {
		sb.WriteByte(prefix)
		sb.WriteString(s)
		if !strings.HasSuffix(s, "\n") {
			sb.WriteString("\n\\ No newline at end of file\n")
		}
	}
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		// Hunk: from ctx lines before the change to ctx lines after the last
		// change that is within 2*ctx of the previous one.
		start := max(0, i-ctx)
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*ctx {
				end = min(len(ops), end+ctx)
				break
			}
			end = run
		}
		aStart, bStart, aLen, bLen := -1, -1, 0, 0
		for _, o := range ops[start:end] {
			switch o.kind {
			case ' ':
				if aStart < 0 {
					aStart, bStart = o.ai, o.bi
				}
				aLen++
				bLen++
			case '-':
				if aStart < 0 {
					aStart, bStart = o.ai, o.bi
				}
				aLen++
			case '+':
				if aStart < 0 {
					aStart, bStart = o.ai, o.bi
				}
				bLen++
			}
		}
		fmt.Fprintf(&sb, "@@ -%s +%s @@\n", rng(aStart, aLen), rng(bStart, bLen))
		for _, o := range ops[start:end] {
			switch o.kind {
			case ' ':
				line(' ', a[o.ai])
			case '-':
				line('-', a[o.ai])
			case '+':
				line('+', b[o.bi])
			}
		}
		i = end
	}
	return sb.String()
}

func rng(start, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	if n == 1 {
		return fmt.Sprintf("%d", start+1)
	}
	return fmt.Sprintf("%d,%d", start+1, n)
}
