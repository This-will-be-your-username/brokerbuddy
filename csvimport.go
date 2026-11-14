package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ---- Column mapping -------------------------------------------------------

// mapping says which CSV column means what. -1 = not used.
type mapping struct {
	Date, Desc, Desc2, Amount, Debit, Credit, Category int
	Split                                              bool   // separate debit/credit columns instead of one signed amount column
	Invert                                             bool   // single-amount mode: true if positive numbers mean money spent
	Layout                                             string // Go time layout for the date column
}

type dateLayout struct{ Layout, Label string }

// Order matters: day-first is tried before month-first, so ambiguous dates read as D/M (AU/UK/NZ).
var dateLayouts = []dateLayout{
	{"2006-01-02", "2024-12-31"},
	{"2/1/2006", "31/12/2024 (day first)"},
	{"1/2/2006", "12/31/2024 (month first)"},
	{"2-1-2006", "31-12-2024"},
	{"2/1/06", "31/12/24 (day first)"},
	{"1/2/06", "12/31/24 (month first)"},
	{"2 Jan 2006", "31 Dec 2024"},
	{"2 January 2006", "31 December 2024"},
	{"2-Jan-2006", "31-Dec-2024"},
	{"Jan 2, 2006", "Dec 31, 2024"},
	{"2006/01/02", "2024/12/31"},
	{"20060102", "20241231"},
	{"2006-01-02 15:04:05", "2024-12-31 13:45:00"},
	{"2/1/2006 15:04", "31/12/2024 13:45"},
	{"2/1/2006 15:04:05", "31/12/2024 13:45:00"},
	{"2006-01-02T15:04:05Z07:00", "ISO 8601 timestamp"},
}

// ---- Reading the file -----------------------------------------------------

// readCSV decodes bytes (UTF-8 BOM / Latin-1 tolerant), sniffs the delimiter, and returns
// non-empty rows padded to equal width.
func readCSV(b []byte) ([][]string, error) {
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(b) { // legacy Windows exports: treat as Latin-1
		r := make([]rune, len(b))
		for i, c := range b {
			r[i] = rune(c)
		}
		b = []byte(string(r))
	}
	delim := sniffDelimiter(b)
	rd := csv.NewReader(bytes.NewReader(b))
	rd.Comma = delim
	rd.LazyQuotes = true
	rd.FieldsPerRecord = -1
	all, err := rd.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("not a readable CSV: %w", err)
	}
	var rows [][]string
	width := 0
	for _, r := range all {
		empty := true
		for i := range r {
			r[i] = strings.TrimSpace(r[i])
			if r[i] != "" {
				empty = false
			}
		}
		if empty {
			continue
		}
		rows = append(rows, r)
		width = max(width, len(r))
	}
	for i := range rows {
		for len(rows[i]) < width {
			rows[i] = append(rows[i], "")
		}
	}
	return rows, nil
}

func sniffDelimiter(b []byte) rune {
	lines := bytes.SplitN(b, []byte("\n"), 12)
	best, bestN := ',', 0
	for _, d := range []rune{',', ';', '\t', '|'} {
		n := 0
		for _, l := range lines {
			n += bytes.Count(l, []byte(string(d)))
		}
		if n > bestN {
			best, bestN = d, n
		}
	}
	return best
}

// ---- Parsing cells --------------------------------------------------------

func parseAmount(s string) (float64, bool) {
	s = strings.NewReplacer("$", "", "£", "", "€", "", ",", "", " ", "", " ", "").Replace(strings.TrimSpace(s))
	if s == "" {
		return 0, false
	}
	neg := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") { // accounting style (12.34)
		neg, s = true, s[1:len(s)-1]
	}
	if strings.HasSuffix(s, "-") { // 12.34-
		neg, s = true, strings.TrimSuffix(s, "-")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	if neg {
		v = -math.Abs(v)
	}
	return v, true
}

func parseDate(layout, s string) (time.Time, bool) {
	t, err := time.Parse(layout, strings.TrimSpace(s))
	return t, err == nil
}

func anyDate(s string) bool {
	for _, l := range dateLayouts {
		if _, ok := parseDate(l.Layout, s); ok {
			return true
		}
	}
	return false
}

// A data row has a date in one cell and a number in a *different* cell.
func isDataRow(row []string) bool {
	di := -1
	for i, c := range row {
		if anyDate(c) {
			di = i
			break
		}
	}
	if di < 0 {
		return false
	}
	for i, c := range row {
		if i != di {
			if _, ok := parseAmount(c); ok {
				return true
			}
		}
	}
	return false
}

// splitHeader skips any preamble (account info lines etc.) and finds the header row.
func splitHeader(rows [][]string) (header []string, data [][]string, err error) {
	first := -1
	for i, r := range rows {
		if isDataRow(r) {
			first = i
			break
		}
	}
	if first < 0 {
		return nil, nil, errors.New("couldn't find any rows with both a date and an amount")
	}
	if first > 0 {
		header = rows[first-1]
	} else {
		header = make([]string, len(rows[0]))
		for i := range header {
			header[i] = fmt.Sprintf("Column %d", i+1)
		}
	}
	return header, rows[first:], nil
}

// ---- Guessing -------------------------------------------------------------

var (
	reDate   = regexp.MustCompile(`(?i)date`)
	reDesc   = regexp.MustCompile(`(?i)descr|narrat|detail|payee|merchant|memo|particular|reference|transaction$`)
	reDebit  = regexp.MustCompile(`(?i)debit|withdraw|money out|paid out|spent`)
	reCredit = regexp.MustCompile(`(?i)credit|deposit|money in|paid in|received`)
	reAmount = regexp.MustCompile(`(?i)amount|value|^amt`)
	reCat    = regexp.MustCompile(`(?i)categor`)
	reSkip   = regexp.MustCompile(`(?i)balance`)
)

func detectLayout(samples []string) string {
	best, bestRate := "", 0.0
	for _, l := range dateLayouts {
		ok, n := 0, 0
		for _, s := range samples {
			if s == "" {
				continue
			}
			n++
			if _, p := parseDate(l.Layout, s); p {
				ok++
			}
		}
		if n == 0 {
			continue
		}
		if rate := float64(ok) / float64(n); rate > bestRate+1e-9 { // strict: earlier layout wins ties
			best, bestRate = l.Layout, rate
		}
	}
	if bestRate < 0.5 {
		return ""
	}
	return best
}

func column(data [][]string, c int) []string {
	out := make([]string, 0, len(data))
	for _, r := range data {
		out = append(out, r[c])
	}
	return out
}

func numericRate(vals []string) float64 {
	n, ok := 0, 0
	for _, v := range vals {
		if v == "" {
			continue
		}
		n++
		if _, p := parseAmount(v); p {
			ok++
		}
	}
	if n == 0 {
		return 0
	}
	return float64(ok) / float64(n)
}

// guessMapping uses header names first, then falls back to looking at the cell contents.
func guessMapping(header []string, data [][]string) mapping {
	m := mapping{-1, -1, -1, -1, -1, -1, -1, false, false, ""}
	find := func(re *regexp.Regexp, skip ...int) int {
		for i, h := range header {
			if reSkip.MatchString(h) || !re.MatchString(h) {
				continue
			}
			used := false
			for _, s := range skip {
				used = used || s == i
			}
			if !used {
				return i
			}
		}
		return -1
	}
	m.Date = find(reDate)
	m.Debit = find(reDebit)
	m.Credit = find(reCredit, m.Debit)
	m.Category = find(reCat)
	m.Amount = find(reAmount, m.Debit, m.Credit)
	m.Desc = find(reDesc, m.Date)
	m.Split = m.Debit >= 0 && m.Credit >= 0
	if !m.Split {
		m.Debit, m.Credit = -1, -1
	}

	// Content-based fallbacks (headerless or oddly-named files).
	if m.Date < 0 {
		for c := range header {
			if detectLayout(column(data, c)) != "" && !reSkip.MatchString(header[c]) {
				m.Date = c
				break
			}
		}
	}
	if !m.Split && m.Amount < 0 {
		for c := range header {
			if c != m.Date && !reSkip.MatchString(header[c]) && numericRate(column(data, c)) >= 0.8 {
				m.Amount = c
				break
			}
		}
	}
	if m.Desc < 0 {
		bestLen := 0.0
		for c := range header {
			if c == m.Date || c == m.Amount || c == m.Debit || c == m.Credit || c == m.Category || reSkip.MatchString(header[c]) {
				continue
			}
			vals := column(data, c)
			if numericRate(vals) > 0.5 {
				continue
			}
			total := 0
			for _, v := range vals {
				total += len(v)
			}
			if avg := float64(total) / float64(max(len(vals), 1)); avg > bestLen {
				m.Desc, bestLen = c, avg
			}
		}
	}
	if m.Date >= 0 {
		m.Layout = detectLayout(column(data, m.Date))
	}
	// Most accounts show spending as negative; if nearly everything is positive, it's probably a
	// card statement where purchases are positive.
	if !m.Split && m.Amount >= 0 {
		pos, n := 0, 0
		for _, v := range column(data, m.Amount) {
			if f, ok := parseAmount(v); ok {
				n++
				if f >= 0 {
					pos++
				}
			}
		}
		m.Invert = n > 0 && float64(pos)/float64(n) >= 0.9
	}
	return m
}

// signature identifies a bank's CSV format so a confirmed mapping can be reused.
func signature(header []string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.Join(header, "\x1f"))))
	return hex.EncodeToString(h[:8])
}

func (m mapping) valid(width int) error {
	in := func(c int) bool { return c >= 0 && c < width }
	switch {
	case !in(m.Date):
		return errors.New("choose the date column")
	case m.Layout == "":
		return errors.New("choose the date format")
	case !in(m.Desc):
		return errors.New("choose the description column")
	case m.Split && !(in(m.Debit) && in(m.Credit)):
		return errors.New("choose both the debit and credit columns")
	case !m.Split && !in(m.Amount):
		return errors.New("choose the amount column")
	}
	return nil
}

// ---- Converting rows ------------------------------------------------------

type csvTxn struct {
	Date, Desc, Cat string
	Amount          float64 // internal convention: positive = money out
}

var spaces = regexp.MustCompile(`\s+`)

func convertRows(data [][]string, m mapping) (out []csvTxn, skipped int) {
	cell := func(r []string, c int) string {
		if c < 0 || c >= len(r) {
			return ""
		}
		return r[c]
	}
	for _, r := range data {
		d, ok := parseDate(m.Layout, cell(r, m.Date))
		if !ok {
			skipped++ // footers, totals, junk
			continue
		}
		var amt float64
		if m.Split {
			dv, dok := parseAmount(cell(r, m.Debit))
			cv, cok := parseAmount(cell(r, m.Credit))
			if !dok && !cok {
				skipped++
				continue
			}
			amt = math.Abs(dv) - math.Abs(cv)
		} else {
			v, ok := parseAmount(cell(r, m.Amount))
			if !ok {
				skipped++
				continue
			}
			amt = -v
			if m.Invert {
				amt = v
			}
		}
		desc := strings.TrimSpace(cell(r, m.Desc) + " " + cell(r, m.Desc2))
		desc = spaces.ReplaceAllString(desc, " ")
		if desc == "" {
			desc = "(no description)"
		}
		t := csvTxn{Date: d.Format("2006-01-02"), Desc: desc, Amount: math.Round(amt*100) / 100}
		if c := cell(r, m.Category); c != "" {
			t.Cat = "csv:" + c
		}
		out = append(out, t)
	}
	return
}

// txnIDs gives each row a stable ID so re-importing an overlapping export doesn't duplicate.
// Genuinely identical rows (same day, amount, text) are told apart by occurrence number.
func txnIDs(account string, txns []csvTxn) []string {
	seen := map[string]int{}
	ids := make([]string, len(txns))
	for i, t := range txns {
		key := fmt.Sprintf("%s|%s|%.2f|%s", account, t.Date, t.Amount, strings.ToLower(t.Desc))
		seen[key]++
		h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", key, seen[key])))
		ids[i] = "csv_" + hex.EncodeToString(h[:12])
	}
	return ids
}
