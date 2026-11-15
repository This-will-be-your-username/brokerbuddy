package main

import (
	"math"
	"sort"
	"strings"
)

// Header detection. Column headings are often vertically centred or wrapped ("Money" / "out"), so
// the heading cells of one table can be spread over two or three text lines. Starting from a line
// that holds a "Date" heading, short number-free fragments on neighbouring lines are collected and
// grouped into columns by horizontal overlap.

type frag struct {
	x0, x1, y float64
	text      string
}

func hasDigit(s string) bool { return strings.ContainsAny(s, "0123456789") }

func shortWords(ln pline) bool {
	if len(ln.span) == 0 || len(ln.span) > 8 {
		return false
	}
	for _, s := range ln.span {
		if hasDigit(s.text) || len(s.text) > 28 {
			return false
		}
	}
	return true
}

func isDateAnchor(ln pline) bool {
	for _, s := range ln.span {
		if !hasDigit(s.text) && len(s.text) <= 24 && headerRole(s.text) == rDate {
			return len(ln.span) >= 2
		}
	}
	return false
}

// findHeaders returns the columns of the first header found and the indexes of every line that
// belongs to a header (headers repeat on each page).
func findHeaders(lines []pline) ([]hdrCol, map[int]bool) {
	used := map[int]bool{}
	var first []hdrCol
	for i, ln := range lines {
		if used[i] || !isDateAnchor(ln) {
			continue
		}
		members := []int{i}
		frags := toFrags(ln)
		// neighbours above, then below, each close to the previous accepted line
		prevY := ln.y
		for j := i - 1; j >= 0 && j >= i-2; j-- {
			c := lines[j]
			if c.page != ln.page || math.Abs(c.y-prevY) > 1.5*ln.fs || !shortWords(c) || used[j] {
				break
			}
			members, frags, prevY = append(members, j), append(frags, toFrags(c)...), c.y
		}
		prevY = ln.y
		for j := i + 1; j < len(lines) && j <= i+2; j++ {
			c := lines[j]
			if c.page != ln.page || math.Abs(c.y-prevY) > 1.5*ln.fs || !shortWords(c) {
				break
			}
			members, frags, prevY = append(members, j), append(frags, toFrags(c)...), c.y
		}
		cols := groupFrags(frags)
		if !validHeader(cols) {
			continue
		}
		for _, m := range members {
			used[m] = true
		}
		if first == nil {
			first = cols
		}
	}
	return first, used
}

func toFrags(ln pline) []frag {
	out := make([]frag, 0, len(ln.span))
	for _, s := range ln.span {
		out = append(out, frag{s.x0, s.x1, ln.y, s.text})
	}
	return out
}

func groupFrags(fr []frag) []hdrCol {
	sort.Slice(fr, func(i, j int) bool { return (fr[i].x0 + fr[i].x1) < (fr[j].x0 + fr[j].x1) })
	type grp struct {
		x0, x1 float64
		f      []frag
	}
	var gs []*grp
	for _, f := range fr {
		placed := false
		for _, g := range gs {
			overlap := f.x0 <= g.x1+3 && f.x1 >= g.x0-3
			centres := math.Abs((f.x0+f.x1)/2-(g.x0+g.x1)/2) < 14
			if overlap || centres {
				g.f = append(g.f, f)
				g.x0, g.x1 = math.Min(g.x0, f.x0), math.Max(g.x1, f.x1)
				placed = true
				break
			}
		}
		if !placed {
			gs = append(gs, &grp{f.x0, f.x1, []frag{f}})
		}
	}
	sort.Slice(gs, func(i, j int) bool { return gs[i].x0 < gs[j].x0 })
	var cols []hdrCol
	for _, g := range gs {
		sort.SliceStable(g.f, func(i, j int) bool { return g.f[i].y > g.f[j].y }) // top line first
		var parts []string
		for _, f := range g.f {
			parts = append(parts, f.text)
		}
		name := strings.Join(parts, " ")
		cols = append(cols, hdrCol{headerRole(name), name, g.x0, g.x1})
	}
	return cols
}

func validHeader(cols []hdrCol) bool {
	have := map[role]bool{}
	for _, c := range cols {
		have[c.role] = true
	}
	return have[rDate] && (have[rDebit] || have[rCredit] || have[rAmount] || have[rDesc])
}

func nearAny(xs []float64, x, tol float64) bool {
	for _, v := range xs {
		if math.Abs(v-x) <= tol {
			return true
		}
	}
	return false
}
