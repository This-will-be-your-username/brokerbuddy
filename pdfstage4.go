package main

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---- 4. dates, signs and output ---------------------------------------------------------------

type period struct{ start, end time.Time }

var (
	rePeriodSep = regexp.MustCompile(`(?i)\s(?:to|until|through|-|–|—)\s`)
	reAnyDate   = regexp.MustCompile(`\b(\d{1,2}[ /\-][A-Za-z]{3,9}[ /\-]\d{4}|\d{1,2}/\d{1,2}/\d{4}|\d{4}-\d{2}-\d{2})\b`)
	reYear      = regexp.MustCompile(`\b(20\d{2})\b`)
)

func toTime(d pdate) time.Time {
	if d.numeric {
		return time.Date(d.y, time.Month(d.b), d.a, 0, 0, 0, 0, time.UTC) // month-first reading; only the year matters here
	}
	return time.Date(d.y, time.Month(d.m), d.d, 0, 0, 0, 0, time.UTC)
}

// statementPeriod finds "1 Oct 2025 to 31 Oct 2025"-style text (or any dated heading) so dates
// printed without a year can be completed.
func statementPeriod(lines []pline) (period, bool) {
	var seen []time.Time
	for _, ln := range lines {
		parts := make([]string, 0, len(ln.span))
		for _, s := range ln.span {
			parts = append(parts, s.text)
		}
		text := strings.Join(parts, " ")
		low := strings.ToLower(text)
		if idx := rePeriodSep.FindStringIndex(text); idx != nil {
			words := strings.Fields(text[:idx[0]])
			for take := 1; take <= 3 && take <= len(words); take++ {
				a, ok1 := peelDate(strings.Join(words[len(words)-take:], " "))
				b, ok2 := peelDate(text[idx[1]:])
				if ok1 && ok2 && a.hasYear && b.hasYear && !a.numeric && !b.numeric {
					s := time.Date(a.y, time.Month(a.m), a.d, 0, 0, 0, 0, time.UTC)
					e := time.Date(b.y, time.Month(b.m), b.d, 0, 0, 0, 0, time.UTC)
					if !e.Before(s) && e.Sub(s) < 400*24*time.Hour {
						return period{s, e}, true
					}
				}
			}
		}
		if strings.Contains(low, "statement") || strings.Contains(low, "closing") || strings.Contains(low, "period") || strings.Contains(low, "as at") {
			for _, w := range reAnyDate.FindAllString(text, -1) {
				if d, ok := peelDate(w); ok && d.hasYear {
					seen = append(seen, toTime(d))
				}
			}
			for _, w := range reYear.FindAllString(text, -1) {
				y, _ := strconv.Atoi(w)
				seen = append(seen, time.Date(y, 6, 30, 0, 0, 0, 0, time.UTC))
			}
		}
	}
	if len(seen) == 0 {
		return period{}, false
	}
	sort.Slice(seen, func(i, j int) bool { return seen[i].Before(seen[j]) })
	return period{seen[0], seen[len(seen)-1]}, true
}

type trow struct {
	date       time.Time
	desc       string
	mag        float64
	dir        int // +1 credit, -1 debit, 0 not marked, -2 written with a minus sign
	orig       int // dir as printed, before the balance check
	hasDebit   bool
	hasCredit  bool
	debit, cr  float64
	balance    float64
	hasBalance bool
}

// resolveSigns sets each single-column amount's direction from how the running balance moved.
// Statements may list newest first, so the order is taken from the dates. It only applies the
// result when most rows agree, so a misread balance column can't flip everything.
func resolveSigns(rows []*trow, opening *float64) bool {
	n := len(rows)
	if n == 0 {
		return false
	}
	order := make([]*trow, n)
	copy(order, rows)
	if rows[0].date.After(rows[n-1].date) { // newest first
		for i := range order {
			order[i] = rows[n-1-i]
		}
	}
	type decision struct {
		r   *trow
		dir int
	}
	var ds []decision
	agree, tried := 0, 0
	var prev *float64
	if opening != nil {
		v := *opening
		prev = &v
	}
	for _, r := range order {
		if r.hasBalance && prev != nil && r.mag > 0 && !r.hasDebit && !r.hasCredit {
			tried++
			delta := r.balance - *prev
			switch {
			case math.Abs(delta-r.mag) < 0.011:
				ds, agree = append(ds, decision{r, 1}), agree+1
			case math.Abs(delta+r.mag) < 0.011:
				ds, agree = append(ds, decision{r, -1}), agree+1
			}
		}
		if r.hasBalance {
			b := r.balance
			prev = &b
		}
	}
	if tried == 0 || float64(agree) < 0.7*float64(tried) {
		return false
	}
	for _, d := range ds {
		d.r.dir = d.dir
	}
	return true
}

func finishRows(res *pdfResult, lines []pline, txns []*prow, opening *float64) (*pdfResult, error) {
	// day-first vs month-first for numeric dates: any part above 12 settles it; default day-first
	dayFirst := true
	for _, t := range txns {
		if t.date.numeric {
			if t.date.a > 12 {
				break
			}
			if t.date.b > 12 {
				dayFirst = false
				break
			}
		}
	}
	per, havePer := statementPeriod(lines)
	yearFor := func(month int) int {
		if !havePer {
			return time.Now().Year()
		}
		if per.end.Year() > per.start.Year() && month >= int(per.start.Month()) {
			return per.start.Year()
		}
		return per.end.Year()
	}

	missingYear := false
	var rows []*trow
	for _, t := range txns {
		d := t.date
		day, mon := d.d, d.m
		if d.numeric {
			if dayFirst {
				day, mon = d.a, d.b
			} else {
				day, mon = d.b, d.a
			}
		}
		year := d.y
		if !d.hasYear {
			year, missingYear = yearFor(mon), true
		}
		dt := time.Date(year, time.Month(mon), day, 0, 0, 0, 0, time.UTC)
		if dt.Month() != time.Month(mon) || dt.Day() != day { // impossible date such as 31 Feb
			continue
		}
		tr := &trow{date: dt, desc: strings.TrimSpace(strings.Join(t.desc, " "))}
		if v, ok := t.amt[rDebit]; ok {
			if m, _, ok := readAmount(v); ok {
				tr.hasDebit, tr.debit = true, m
			}
		}
		if v, ok := t.amt[rCredit]; ok {
			if m, _, ok := readAmount(v); ok {
				tr.hasCredit, tr.cr = true, m
			}
		}
		if v, ok := t.amt[rAmount]; ok {
			if m, dir, ok := readAmount(v); ok {
				tr.mag, tr.dir, tr.orig = m, dir, dir
			}
		}
		if v, ok := t.amt[rBalance]; ok {
			if m, dir, ok := readAmount(v); ok {
				tr.balance, tr.hasBalance = m, true
				if dir == -1 || dir == -2 {
					tr.balance = -m
				}
			}
		}
		rows = append(rows, tr)
	}
	if len(rows) == 0 {
		return nil, errPDFNoTable
	}
	if missingYear {
		if havePer {
			res.Notes = append(res.Notes, fmt.Sprintf("Dates without a year use the statement period (%s to %s).", per.start.Format("2 Jan 2006"), per.end.Format("2 Jan 2006")))
		} else {
			res.Notes = append(res.Notes, "No statement period was found, so dates without a year were given the current year. Check them in the preview.")
		}
	}

	// One amount column and no CR/DR markers: the running balance says which way money moved.
	needs := false
	for _, r := range rows {
		if !r.hasDebit && !r.hasCredit && r.mag > 0 && r.dir != 1 && r.dir != -1 {
			needs = true
		}
	}
	if needs {
		// resolve first, using only the rows that have no explicit direction
		work := make([]*trow, len(rows))
		copy(work, rows)
		if resolveSigns(work, opening) {
			res.Notes = append(res.Notes, "Money in/out was worked out from the running balance.")
			// Rows with no earlier balance to compare (e.g. the first one) follow the convention the
			// statement itself uses: learn it from the rows that were resolved.
			var minusDeb, minusN, plainCred, plainN int
			for _, r := range rows {
				if r.hasDebit || r.hasCredit || r.mag == 0 || (r.dir != 1 && r.dir != -1) {
					continue
				}
				switch r.orig {
				case -2:
					minusN++
					if r.dir == -1 {
						minusDeb++
					}
				case 0:
					plainN++
					if r.dir == 1 {
						plainCred++
					}
				}
			}
			minusMeansDebit := minusN > 0 && float64(minusDeb) >= 0.8*float64(minusN)
			plainMeansCredit := plainN > 0 && float64(plainCred) >= 0.8*float64(plainN)
			guessed := 0
			for _, r := range rows {
				if r.hasDebit || r.hasCredit || r.mag == 0 || r.dir == 1 || r.dir == -1 {
					continue
				}
				switch {
				case r.orig == -2 && minusMeansDebit:
					r.dir = -1
				case r.orig == 0 && plainMeansCredit:
					r.dir = 1
				default:
					r.dir = -1
					guessed++
				}
			}
			if guessed > 0 {
				res.Notes = append(res.Notes, fmt.Sprintf("%d row(s) had no earlier balance to compare with and were assumed to be money out. Check them in the preview.", guessed))
			}
		}
	}

	debitCredit := true
	anyBalance := false
	for _, r := range rows {
		explicit := r.hasDebit || r.hasCredit || (r.mag > 0 && (r.dir == 1 || r.dir == -1))
		if !explicit {
			debitCredit = false
		}
		anyBalance = anyBalance || r.hasBalance
	}
	if debitCredit {
		res.Header = []string{"Date", "Description", "Debit", "Credit"}
	} else {
		res.Header = []string{"Date", "Description", "Amount"}
		res.Notes = append(res.Notes, "This statement shows a single amount column without a debit/credit marker. Check the direction in the preview and tick “positive numbers are money spent” if it is reversed.")
	}
	if anyBalance {
		res.Header = append(res.Header, "Balance")
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
	for _, r := range rows {
		row := []string{r.date.Format("2006-01-02"), r.desc}
		if debitCredit {
			deb, cred := "", ""
			switch {
			case r.hasDebit || r.hasCredit:
				if r.hasDebit {
					deb = f(r.debit)
				}
				if r.hasCredit {
					cred = f(r.cr)
				}
			case r.dir == 1:
				cred = f(r.mag)
			default:
				deb = f(r.mag)
			}
			row = append(row, deb, cred)
		} else {
			amt := f(r.mag)
			if r.dir == -2 || r.dir == -1 {
				amt = "-" + amt
			}
			row = append(row, amt)
		}
		if anyBalance {
			if r.hasBalance {
				row = append(row, f(r.balance))
			} else {
				row = append(row, "")
			}
		}
		res.Rows = append(res.Rows, row)
	}
	return res, nil
}
