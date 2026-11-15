package main

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ledongthuc/pdf"
)

// PDF statement import.
//
// A statement is read in four steps, then handed to the same mapping / preview / de-duplicate
// screens as CSV files:
//
//  1. Text with positions is pulled from every page and rebuilt into lines and cells.
//  2. The transaction table is found: by its header row ("Date  Details  Debit  Credit  Balance")
//     or, when there is no header, from rows that start with a date and end in amounts.
//  3. Numeric columns are located by clustering the x positions of the amounts, then labelled from
//     the header (or by position). Descriptions that wrap onto extra lines are re-joined, and a
//     transaction that only shows its date once per day inherits the date.
//  4. Dates are completed (the year is taken from the statement period) and the money direction
//     is resolved: Debit/Credit columns, CR/DR markers, or the running balance.
//
// The library reports zero glyph widths for many fonts, so text extents are estimated from a
// character-width model. That is accurate enough to separate columns, which are far apart.

var (
	errPDFEncrypted = errors.New("this PDF is password protected: enter its password and upload again")
	errPDFNoText    = errors.New("no text could be read from this PDF. It looks like a scanned image, which needs OCR (not supported). Download the statement as a CSV or a text-based PDF from your bank instead")
	errPDFNoTable   = errors.New("couldn't find a transaction table in this PDF. Try the CSV export from your bank, or check that the PDF is a statement with a date column and amounts")
)

type pdfResult struct {
	Header []string
	Rows   [][]string
	Notes  []string
	Pages  int
}

// ---- 1. text extraction ---------------------------------------------------------------------

type run struct {
	x, y, fs float64
	text     string
	mono     bool // fixed-width text: positions come from character index
}

type span struct {
	x0, x1 float64
	text   string
}

type pline struct {
	page int
	y    float64
	fs   float64
	span []span
}

// Rough Arial/Helvetica advance widths in em, used because the PDF library reports none.
func charEm(r rune) float64 {
	switch {
	case r >= '0' && r <= '9':
		return 0.556
	case strings.ContainsRune(" .,:;!|'`ijl/\\I", r):
		return 0.278
	case strings.ContainsRune("ft()[]-\"", r):
		return 0.333
	case strings.ContainsRune("mMwW%", r):
		return 0.85
	case r >= 'A' && r <= 'Z':
		return 0.68
	case r == '$' || r == '#' || r == '+' || r == '=' || r == '<' || r == '>':
		return 0.56
	case r == '&':
		return 0.667
	default:
		return 0.52
	}
}

var monoFont = regexp.MustCompile(`(?i)courier|mono|consol|typewriter|lucida console|menlo|fixed`)

// runWidth: fixed-width text (monospace font, or columns made of runs of spaces) advances
// 0.6 em per character; everything else uses the proportional estimate.
func runWidth(r run) float64 {
	if r.mono || multiSpace.MatchString(r.text) {
		return float64(len([]rune(r.text))) * 0.6 * r.fs
	}
	return estWidth(r.text, r.fs)
}

func estWidth(s string, fs float64) float64 {
	w := 0.0
	for _, r := range s {
		w += charEm(r)
	}
	return w * fs
}

// groupRuns rebuilds text strings: glyphs from one text-showing operation share the same X.
func groupRuns(items []pdf.Text) []run {
	var out []run
	for _, t := range items {
		if t.S == "" {
			continue
		}
		if n := len(out); n > 0 {
			c := &out[n-1]
			if c.x == t.X && c.y == t.Y && c.fs == t.FontSize {
				c.text += t.S
				continue
			}
		}
		out = append(out, run{t.X, t.Y, t.FontSize, t.S, monoFont.MatchString(t.Font)})
	}
	return out
}

var multiSpace = regexp.MustCompile(`\s{2,}`)

// buildLines turns one page's runs into lines of cell-sized spans, top to bottom.
func buildLines(page int, runs []run) []pline {
	sort.SliceStable(runs, func(i, j int) bool {
		if math.Abs(runs[i].y-runs[j].y) > 0.35*math.Max(runs[i].fs, 6) {
			return runs[i].y > runs[j].y
		}
		return runs[i].x < runs[j].x
	})
	var lines []pline
	var cur []run
	flush := func() {
		if len(cur) == 0 {
			return
		}
		sort.SliceStable(cur, func(i, j int) bool { return cur[i].x < cur[j].x })
		fs := cur[0].fs
		ln := pline{page: page, y: cur[0].y, fs: fs}
		var sp *span
		for _, r := range cur {
			w := runWidth(r)
			if sp != nil {
				gap := r.x - sp.x1
				if gap <= 1.1*fs {
					// Widths are estimated, so only a gap clearly bigger than the estimate's error is a space.
					if gap > math.Max(0.25*fs, 0.08*(sp.x1-sp.x0)) && !strings.HasSuffix(sp.text, " ") && !strings.HasPrefix(r.text, " ") {
						sp.text += " "
					}
					sp.text += r.text
					sp.x1 = r.x + w
					continue
				}
				ln.span = append(ln.span, *sp)
			}
			sp = &span{r.x, r.x + w, r.text}
		}
		if sp != nil {
			ln.span = append(ln.span, *sp)
		}
		ln.span = splitWide(ln.span, fs)
		if len(ln.span) > 0 {
			lines = append(lines, ln)
		}
		cur = nil
	}
	for _, r := range runs {
		if len(cur) > 0 && math.Abs(cur[0].y-r.y) > 0.35*math.Max(cur[0].fs, 6) {
			flush()
		}
		cur = append(cur, r)
	}
	flush()
	return lines
}

// splitWide breaks spans on runs of 2+ spaces (columns in text-style statements) and trims them.
func splitWide(in []span, fs float64) []span {
	var out []span
	for _, s := range in {
		mono := multiSpace.MatchString(s.text)
		pos := func(prefix string) float64 {
			if mono {
				return float64(len([]rune(prefix))) * 0.6 * fs
			}
			return estWidth(prefix, fs)
		}
		idx := multiSpace.FindAllStringIndex(s.text, -1)
		start := 0
		emit := func(from, to int) {
			seg := s.text[from:to]
			lead := len(seg) - len(strings.TrimLeft(seg, " \t"))
			t := strings.TrimSpace(seg)
			if t == "" {
				return
			}
			x0 := s.x0 + pos(s.text[:from+lead])
			out = append(out, span{x0, x0 + pos(t), t})
		}
		for _, m := range idx {
			emit(start, m[0])
			start = m[1]
		}
		emit(start, len(s.text))
	}
	return out
}

func extractLines(data []byte, password string) (lines []pline, pages int, err error) {
	defer func() {
		if x := recover(); x != nil {
			err = fmt.Errorf("couldn't read this PDF (%v)", x)
		}
	}()
	tried := false
	r, err := pdf.NewReaderEncrypted(bytes.NewReader(data), int64(len(data)), func() string {
		if tried || password == "" {
			return ""
		}
		tried = true
		return password
	})
	if err != nil {
		if errors.Is(err, pdf.ErrInvalidPassword) || strings.Contains(err.Error(), "password") {
			return nil, 0, errPDFEncrypted
		}
		return nil, 0, fmt.Errorf("couldn't read this PDF: %w", err)
	}
	pages = r.NumPage()
	total := 0
	for i := 1; i <= pages; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		runs := groupRuns(p.Content().Text)
		total += len(runs)
		lines = append(lines, buildLines(i, runs)...)
	}
	if total == 0 {
		return nil, pages, errPDFNoText
	}
	return lines, pages, nil
}

// ---- dates ------------------------------------------------------------------------------------

var monthNum = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "sept": 9, "oct": 10, "nov": 11, "dec": 12,
	"january": 1, "february": 2, "march": 3, "april": 4, "june": 6, "july": 7, "august": 8, "september": 9, "october": 10, "november": 11, "december": 12}

type pdate struct {
	d, m, y int
	hasYear bool
	numeric bool // written as 12/01/2025: day-first or month-first still to be decided
	a, b    int  // the two numeric parts as written
	textLen int
}

var (
	reNumDate = regexp.MustCompile(`^(\d{1,2})[/\-.](\d{1,2})[/\-.](\d{4}|\d{2})\b`)
	reNumNoYr = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})(?:\s|$)`)
	reISODate = regexp.MustCompile(`^(\d{4})[-/.](\d{1,2})[-/.](\d{1,2})\b`)
	reDMonY   = regexp.MustCompile(`(?i)^(\d{1,2})[ \-]?([A-Za-z]{3,9})\.?,?(?:[ \-]?(\d{4}|\d{2})\b)?`)
	reMonDY   = regexp.MustCompile(`(?i)^([A-Za-z]{3,9})\.?\s+(\d{1,2})(?:st|nd|rd|th)?,?(?:\s+(\d{4})\b)?`)
)

func fullYear(y int) int {
	if y < 100 {
		return 2000 + y
	}
	return y
}

// peelDate reads a date at the START of s and returns it with the length consumed.
func peelDate(s string) (pdate, bool) {
	s = strings.TrimSpace(s)
	if m := reISODate.FindStringSubmatch(s); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		if mo >= 1 && mo <= 12 && d >= 1 && d <= 31 {
			return pdate{d: d, m: mo, y: y, hasYear: true, textLen: len(m[0])}, true
		}
	}
	if m := reNumDate.FindStringSubmatch(s); m != nil {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		y, _ := strconv.Atoi(m[3])
		if a >= 1 && a <= 31 && b >= 1 && b <= 31 && (a <= 12 || b <= 12) {
			return pdate{y: fullYear(y), hasYear: true, numeric: true, a: a, b: b, textLen: len(m[0])}, true
		}
	}
	if m := reDMonY.FindStringSubmatch(s); m != nil {
		if mo, ok := monthNum[strings.ToLower(m[2])]; ok {
			d, _ := strconv.Atoi(m[1])
			pd := pdate{d: d, m: mo, textLen: len(m[0])}
			if d >= 1 && d <= 31 {
				if m[3] != "" {
					y, _ := strconv.Atoi(m[3])
					pd.y, pd.hasYear = fullYear(y), true
				}
				return pd, true
			}
		}
	}
	if m := reMonDY.FindStringSubmatch(s); m != nil {
		if mo, ok := monthNum[strings.ToLower(m[1])]; ok {
			d, _ := strconv.Atoi(m[2])
			pd := pdate{d: d, m: mo, textLen: len(m[0])}
			if d >= 1 && d <= 31 {
				if m[3] != "" {
					y, _ := strconv.Atoi(m[3])
					pd.y, pd.hasYear = fullYear(y), true
				}
				return pd, true
			}
		}
	}
	if m := reNumNoYr.FindStringSubmatch(s); m != nil {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		if a >= 1 && a <= 31 && b >= 1 && b <= 31 && (a <= 12 || b <= 12) {
			return pdate{numeric: true, a: a, b: b, textLen: len(m[0])}, true
		}
	}
	return pdate{}, false
}

// ---- amounts ----------------------------------------------------------------------------------

var reAmountTok = regexp.MustCompile(`^[-+(]?\$?\s?\d{1,3}(,\d{3})*\.\d{2}\)?-?(\s?(CR|DR|Cr|Dr|cr|dr))?$|^[-+(]?\$?\s?\d+\.\d{2}\)?-?(\s?(CR|DR|Cr|Dr|cr|dr))?$`)
var reCRDR = regexp.MustCompile(`(?i)\s?(CR|DR)$`)

func isAmountSpan(s string) bool { return reAmountTok.MatchString(strings.TrimSpace(s)) }

// readAmount returns the magnitude and an explicit direction: +1 credit, -1 debit, 0 unknown.
func readAmount(s string) (mag float64, dir int, ok bool) {
	s = strings.TrimSpace(s)
	if m := reCRDR.FindStringSubmatch(s); m != nil {
		if strings.EqualFold(m[1], "CR") {
			dir = 1
		} else {
			dir = -1
		}
		s = strings.TrimSpace(reCRDR.ReplaceAllString(s, ""))
	}
	v, ok := parseAmount(s)
	if !ok {
		return 0, 0, false
	}
	if dir == 0 && v < 0 {
		dir = -2 // written with a minus sign (meaning depends on the account type)
	}
	return math.Abs(v), dir, true
}

// ---- 2. header and table detection ------------------------------------------------------------

type role string

const (
	rDate, rDesc, rDebit, rCredit, rAmount, rBalance, rOther role = "date", "desc", "debit", "credit", "amount", "balance", "other"
)

var (
	reHdrBalance = regexp.MustCompile(`(?i)balance`)
	reHdrDebit   = regexp.MustCompile(`(?i)debit|withdraw|money out|paid out|payments? out|\bdr\b|charges`)
	reHdrCredit  = regexp.MustCompile(`(?i)credit|deposit|money in|paid in|payments? in|\bcr\b`)
	reHdrAmount  = regexp.MustCompile(`(?i)amount|\bvalue\b|\btotal\b`)
	reHdrDesc    = regexp.MustCompile(`(?i)descr|details|narrat|particulars|transaction|payee|reference|memo`)
	reHdrDate    = regexp.MustCompile(`(?i)date|posted|^day$`)
)

func headerRole(text string) role {
	t := strings.TrimSpace(text)
	switch {
	case reHdrBalance.MatchString(t):
		return rBalance
	case reHdrDebit.MatchString(t):
		return rDebit
	case reHdrCredit.MatchString(t):
		return rCredit
	case reHdrAmount.MatchString(t):
		return rAmount
	case reHdrDate.MatchString(t):
		return rDate
	case reHdrDesc.MatchString(t):
		return rDesc
	}
	return rOther
}

type hdrCol struct {
	role   role
	name   string
	x0, x1 float64
}

var reEnd = regexp.MustCompile(`(?i)^(closing balance|total (debits|credits|payments|purchases|fees|interest)|balance (carried|c/f)|end of (statement|transactions)|summary|interest (rate|charged))`)
var reOpening = regexp.MustCompile(`(?i)opening balance|balance (brought )?forward|balance b/f|previous balance`)

// ---- 3. building rows ------------------------------------------------------------------------

type prow struct {
	date    pdate
	dateRaw string
	desc    []string
	amt     map[role]string
	opening bool
	page    int
}

type numCol struct {
	pos  float64
	axis int // 0 = right edge, 1 = left edge
	role role
}

func clusterAxis(vals []float64, tol float64) []float64 {
	sort.Float64s(vals)
	var out []float64
	var sum float64
	var n int
	for _, v := range vals {
		if n > 0 && v-sum/float64(n) > tol {
			out = append(out, sum/float64(n))
			sum, n = 0, 0
		}
		sum += v
		n++
	}
	if n > 0 {
		out = append(out, sum/float64(n))
	}
	return out
}

// findNumericColumns clusters the x positions of every amount found on date-led lines.
func findNumericColumns(lines []pline, dateX0 float64, fs float64) (cols []float64, axis int) {
	var right, left []float64
	for _, ln := range lines {
		first := ln.span[0]
		if _, ok := peelDate(first.text); !ok || math.Abs(first.x0-dateX0) > 2*fs+8 {
			continue
		}
		for _, s := range ln.span[1:] {
			if isAmountSpan(s.text) {
				right = append(right, s.x1)
				left = append(left, s.x0)
			}
		}
	}
	tol := math.Max(3, 0.5*fs)
	cr, cl := clusterAxis(right, tol), clusterAxis(left, tol)
	if len(cl) < len(cr) {
		return cl, 1
	}
	return cr, 0
}

func nearestCol(cols []float64, axis int, s span, tol float64) int {
	pos := s.x1
	if axis == 1 {
		pos = s.x0
	}
	best, bestD := -1, 1e9
	for i, c := range cols {
		if d := math.Abs(c - pos); d < bestD {
			best, bestD = i, d
		}
	}
	if bestD > tol {
		return -1
	}
	return best
}

// labelColumns assigns a role to each numeric column from the header, or by position when headerless.
func labelColumns(cols []float64, axis int, hdr []hdrCol, lines []pline, dateX0 float64) []role {
	n := len(cols)
	roles := make([]role, n)
	var money []hdrCol
	for _, h := range hdr {
		if h.role == rDebit || h.role == rCredit || h.role == rAmount || h.role == rBalance {
			money = append(money, h)
		}
	}
	if len(money) > 0 {
		used := map[int]bool{}
		for i, c := range cols {
			best, bestD := -1, 1e9
			for j, h := range money {
				if used[j] {
					continue
				}
				d := math.Min(math.Abs(h.x1-c), math.Min(math.Abs(h.x0-c), math.Abs((h.x0+h.x1)/2-c)))
				if d < bestD {
					best, bestD = j, d
				}
			}
			if best >= 0 {
				roles[i] = money[best].role
				used[best] = true
			} else {
				roles[i] = rOther
			}
		}
		_ = axis
		return roles
	}
	// headerless: decide from the shape of the data
	twoPerRow, rows := 0, 0
	for _, ln := range lines {
		if _, ok := peelDate(ln.span[0].text); !ok || math.Abs(ln.span[0].x0-dateX0) > 20 {
			continue
		}
		k := 0
		for _, s := range ln.span[1:] {
			if isAmountSpan(s.text) {
				k++
			}
		}
		if k > 0 {
			rows++
		}
		if k >= 2 {
			twoPerRow++
		}
	}
	switch {
	case n == 1:
		roles[0] = rAmount
	case n == 2 && twoPerRow*2 >= rows:
		roles[0], roles[1] = rAmount, rBalance
	case n == 2:
		roles[0], roles[1] = rDebit, rCredit
	case n >= 3:
		for i := range roles {
			roles[i] = rOther
		}
		roles[n-3], roles[n-2], roles[n-1] = rDebit, rCredit, rBalance
	}
	return roles
}

var skipLine = regexp.MustCompile(`(?i)^(page \d+( of \d+)?|continued|statement (no|number)|account (no|number)|bsb)\b`)

// ---- main entry -------------------------------------------------------------------------------

func parsePDFStatement(data []byte, password string) (*pdfResult, error) {
	lines, pages, err := extractLines(data, password)
	if err != nil {
		return nil, err
	}
	res := &pdfResult{Pages: pages}

	// Find the table: header row if there is one, otherwise date-led lines.
	hdr, hdrIdx := findHeaders(lines)

	// date column x and font size from the first plausible data line
	dateX0, fs := 0.0, 9.0
	var secondaryDates []float64
	found := false
	for i, ln := range lines {
		if hdrIdx[i] {
			continue
		}
		if _, ok := peelDate(ln.span[0].text); !ok {
			continue
		}
		hasAmt := false
		for _, s := range ln.span[1:] {
			if isAmountSpan(s.text) {
				hasAmt = true
			}
		}
		if hasAmt || (len(ln.span) == 1 && len(ln.span[0].text) > 10) {
			dateX0, fs, found = ln.span[0].x0, ln.fs, true
			break
		}
	}
	if hdr != nil {
		// The leftmost date heading is the transaction date; any further date columns
		// (e.g. "Posted date") are ignored when reading rows.
		first := true
		for _, h := range hdr {
			if h.role != rDate {
				continue
			}
			if first {
				dateX0, first = h.x0, false
			} else {
				secondaryDates = append(secondaryDates, h.x0)
			}
		}
	}
	if !found {
		return nil, errPDFNoTable
	}

	numCols, axis := findNumericColumns(lines, dateX0, fs)
	if len(numCols) == 0 {
		return nil, errPDFNoTable
	}
	roles := labelColumns(numCols, axis, hdr, lines, dateX0)
	if hdr != nil {
		res.Notes = append(res.Notes, "Table found by its header row.")
	} else {
		res.Notes = append(res.Notes, "No header row found; columns were worked out from the layout. Check the mapping below.")
	}

	// Walk the lines, building rows.
	var rows []*prow
	var cur *prow
	inTable := hdr == nil // headerless: whole document; with a header: after it appears
	tol := math.Max(8, 1.0*fs)
	for i, ln := range lines {
		if hdrIdx[i] {
			inTable = true
			cur = nil
			continue
		}
		if !inTable {
			continue
		}
		first := ln.span[0]
		joined := strings.TrimSpace(first.text)
		if reEnd.MatchString(joined) {
			inTable = hdr == nil
			cur = nil
			continue
		}
		if skipLine.MatchString(joined) {
			continue
		}
		d, isDate := peelDate(first.text)
		isDate = isDate && math.Abs(first.x0-dateX0) <= 2*fs+8

		// Page furniture (legal footer, page numbers, web addresses, small print) is not part of any
		// transaction. Without this it was appended to the description of the last row on each page.
		if !isDate {
			var all []string
			for _, s := range ln.span {
				all = append(all, s.text)
			}
			if footerRe.MatchString(strings.Join(all, " ")) || (ln.fs > 0 && ln.fs < 0.8*fs) {
				continue
			}
		}

		var amts = map[role]string{}
		var text []string
		for si, s := range ln.span {
			if si == 0 && isDate {
				if rest := strings.TrimSpace(first.text[d.textLen:]); rest != "" {
					text = append(text, rest)
				}
				continue
			}
			if pd, ok := peelDate(s.text); ok && pd.textLen == len(strings.TrimSpace(s.text)) && nearAny(secondaryDates, s.x0, 25) {
				continue // value in a second date column
			}
			if isAmountSpan(s.text) {
				if ci := nearestCol(numCols, axis, s, tol); ci >= 0 && roles[ci] != rOther {
					if _, taken := amts[roles[ci]]; !taken {
						amts[roles[ci]] = strings.TrimSpace(s.text)
						continue
					}
				}
			}
			if (strings.EqualFold(s.text, "CR") || strings.EqualFold(s.text, "DR")) && len(amts) > 0 { // detached marker
				for rl, v := range amts {
					if !reCRDR.MatchString(v) && (rl == rAmount || rl == rBalance) {
						amts[rl] = v + " " + strings.ToUpper(s.text)
						break
					}
				}
				continue
			}
			text = append(text, s.text)
		}

		switch {
		case isDate:
			cur = &prow{date: d, dateRaw: first.text[:d.textLen], desc: text, amt: amts, page: ln.page}
			rows = append(rows, cur)
		case cur != nil && moneyCount(amts) > 0 && len(text) > 0 && cur.moneyCount() > 0:
			// a new transaction that shares the previous date (date shown once per day)
			nr := &prow{date: cur.date, dateRaw: cur.dateRaw, desc: text, amt: amts, page: ln.page}
			rows = append(rows, nr)
			cur = nr
		case cur != nil && ln.page >= cur.page:
			// continuation line: more description, or the amounts that wrapped to the next line
			cur.desc = append(cur.desc, text...)
			for rl, v := range amts {
				if _, ok := cur.amt[rl]; !ok {
					cur.amt[rl] = v
				}
			}
		}
	}

	// Drop opening/closing balance lines and rows with no transaction amount.
	var txns []*prow
	var opening *float64
	for _, r := range rows {
		desc := strings.TrimSpace(strings.Join(r.desc, " "))
		_, hasD := r.amt[rDebit]
		_, hasC := r.amt[rCredit]
		_, hasA := r.amt[rAmount]
		if reOpening.MatchString(desc) || (!hasD && !hasC && !hasA) {
			if v, ok := r.amt[rBalance]; ok && opening == nil && reOpening.MatchString(desc) {
				if m, dir, ok := readAmount(v); ok {
					f := m
					if dir == -1 || dir == -2 {
						f = -m
					}
					opening = &f
				}
			}
			continue
		}
		if strings.HasPrefix(strings.ToLower(desc), "closing balance") {
			continue
		}
		txns = append(txns, r)
	}
	if len(txns) == 0 {
		return nil, errPDFNoTable
	}
	return finishRows(res, lines, txns, opening)
}

func moneyCount(m map[role]string) int {
	n := 0
	for rl := range m {
		if rl != rBalance {
			n++
		}
	}
	return n
}
func (r *prow) moneyCount() int { return moneyCount(r.amt) }
