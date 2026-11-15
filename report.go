package main

import (
	"encoding/csv"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"
)

type repLine struct {
	Category       string
	Total, Monthly float64
	Pct            float64 // monthly as % of monthly income
	Count          int
}
type repGroup struct {
	Name           string
	Lines          []repLine
	Total, Monthly float64
}
type repFlag struct{ Level, Text string }

type report struct {
	SkippedMonth                 bool // the current month was left out of the analysis
	Months                       int
	From, To                     string
	SpanMonths                   float64
	Groups                       []repGroup // income first, then spending groups, then neutral
	IncomeM, ExpensesM, SurplusM float64
	Flags                        []repFlag
	Empty                        bool
}

func (a *app) buildReport(months int) (*report, error) { return a.buildReportOpt(months, false) }

// buildReportOpt analyses the last `months` months of data. With skipCurrent, the current calendar
// month (still in progress, so its income and spending are incomplete) is left out entirely and
// the window ends at the last transaction before it.
func (a *app) buildReportOpt(months int, skipCurrent bool) (*report, error) {
	rep := &report{Months: months, SkippedMonth: skipCurrent}
	var minS, maxS *string
	q, args := `SELECT MIN(date), MAX(date) FROM transactions`, []any{}
	if skipCurrent {
		q += ` WHERE date < ?`
		args = append(args, firstOfMonth(time.Now()).Format("2006-01-02"))
	}
	if err := a.db.QueryRow(q, args...).Scan(&minS, &maxS); err != nil || maxS == nil {
		rep.Empty = true
		return rep, err
	}
	maxD, _ := time.Parse("2006-01-02", *maxS)
	minD, _ := time.Parse("2006-01-02", *minS)
	start := maxD.AddDate(0, -months, 0).AddDate(0, 0, 1)
	if start.Before(minD) {
		start = minD
	}
	rep.From, rep.To = start.Format("2006-01-02"), maxD.Format("2006-01-02")
	rawMonths := (maxD.Sub(start).Hours()/24 + 1) / 30.44
	if rawMonths >= 1.5 {
		rep.SpanMonths = math.Round(rawMonths) // whole months: pay cycles land in whole months, so decimals overstate income
	} else {
		rep.SpanMonths = math.Max(1, math.Round(rawMonths*10)/10)
	}

	rows, err := a.db.Query(`SELECT category, SUM(amount), COUNT(*) FROM transactions WHERE date>=? AND date<=? GROUP BY category`, rep.From, rep.To)
	if err != nil {
		return nil, err
	}
	byCat := map[string]repLine{}
	for rows.Next() {
		var l repLine
		rows.Scan(&l.Category, &l.Total, &l.Count)
		byCat[l.Category] = l
	}
	rows.Close()

	order := []string{gIncome, gEss, gDisc, gDebt, gRisk, gNeut}
	groups := map[string]*repGroup{}
	for _, g := range order {
		groups[g] = &repGroup{Name: g}
	}
	for _, ci := range taxonomy {
		l, ok := byCat[ci.Name]
		if !ok {
			continue
		}
		if ci.Group == gIncome {
			l.Total = -l.Total // credits are stored negative
		}
		l.Monthly = l.Total / rep.SpanMonths
		g := groups[ci.Group]
		g.Lines = append(g.Lines, l)
		if ci.Name != "Uncategorized" {
			g.Total += l.Total
			g.Monthly += l.Monthly
		}
	}
	// Anything with a category outside the taxonomy (custom or CSV-supplied) counts as discretionary, so it isn't lost.
	for c, l := range byCat {
		if _, known := groupOf[c]; !known {
			l.Monthly = l.Total / rep.SpanMonths
			g := groups[gDisc]
			g.Lines = append(g.Lines, l)
			g.Total += l.Total
			g.Monthly += l.Monthly
		}
	}
	rep.IncomeM = groups[gIncome].Monthly
	for _, g := range []string{gEss, gDisc, gDebt, gRisk} {
		rep.ExpensesM += groups[g].Monthly
	}
	rep.SurplusM = rep.IncomeM - rep.ExpensesM
	for _, g := range order {
		for i := range groups[g].Lines {
			if rep.IncomeM > 0 {
				groups[g].Lines[i].Pct = groups[g].Lines[i].Monthly / rep.IncomeM * 100
			}
		}
		rep.Groups = append(rep.Groups, *groups[g])
	}

	// Things a lender's assessor tends to circle.
	spent := func(c string) repLine { return byCat[c] }
	add := func(level, f string, args ...any) {
		rep.Flags = append(rep.Flags, repFlag{level, fmt.Sprintf(f, args...)})
	}
	if l := spent("Gambling"); l.Count > 0 {
		add("high", "Gambling: %d transactions, $%.2f. Lenders often ask for an explanation, or want to see none in recent months.", l.Count, l.Total)
	}
	if l := spent("Short-term Credit"); l.Count > 0 {
		add("high", "Short-term / payday credit: %d transactions, $%.2f.", l.Count, l.Total)
	}
	if l := spent("Buy Now Pay Later"); l.Count > 0 {
		add("med", "Buy-now-pay-later: %d transactions, $%.2f. Many lenders count BNPL limits as debt.", l.Count, l.Total)
	}
	var dish int
	a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE date>=? AND date<=? AND (lower(name) LIKE '%dishonour%' OR lower(name) LIKE '%dishonor%' OR lower(name) LIKE '%overdrawn%')`, rep.From, rep.To).Scan(&dish)
	if dish > 0 {
		add("high", "%d dishonour / overdrawn entries — a common red flag.", dish)
	} else if l := spent("Fees & Dishonours"); l.Count > 0 {
		add("low", "Bank fees: %d transactions, $%.2f.", l.Count, l.Total)
	}
	if l := spent("Cash Withdrawals"); l.Total/rep.SpanMonths >= 300 {
		add("med", "Cash withdrawals average $%.0f/month — be ready to explain what the cash is for.", l.Total/rep.SpanMonths)
	}
	if groups[gIncome].Total <= 0 {
		add("med", "No income detected. Add a rule for your employer's name so wages are recognised.")
	} else if spent("Salary & Wages").Count == 0 {
		add("low", "No salary-like credits recognised; income is coming from other sources.")
	}
	if spent("Rent").Count == 0 && spent("Mortgage & Loan Repayments").Count == 0 {
		add("low", "No rent or loan payments found. If you pay rent, add a rule so your rental history shows.")
	}
	if u := spent("Uncategorized"); u.Total > 0 && rep.ExpensesM > 0 && u.Total/rep.SpanMonths/rep.ExpensesM > 0.10 {
		add("low", "$%.0f/month (%.0f%% of spending) is still Uncategorized, so the totals above understate some buckets.",
			u.Total/rep.SpanMonths, u.Total/rep.SpanMonths/rep.ExpensesM*100)
	}
	return rep, nil
}

func monthsParam(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("months"))
	if n < 1 || n > 24 {
		return 3
	}
	return n
}

func (a *app) reportPage(w http.ResponseWriter, r *http.Request) {
	rep, err := a.buildReport(monthsParam(r))
	if err != nil {
		fail(w, err, 500)
		return
	}
	a.tpl.ExecuteTemplate(w, "report.html", rep)
}

func (a *app) reportCSV(w http.ResponseWriter, r *http.Request) {
	rep, err := a.buildReport(monthsParam(r))
	if err != nil {
		fail(w, err, 500)
		return
	}
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="spending-summary.csv"`)
	cw := csv.NewWriter(w)
	cw.Write([]string{"Group", "Category", "Total", "Monthly average", "Transactions", "% of income"})
	for _, g := range rep.Groups {
		for _, l := range g.Lines {
			cw.Write([]string{g.Name, l.Category, fmt.Sprintf("%.2f", l.Total), fmt.Sprintf("%.2f", l.Monthly), strconv.Itoa(l.Count), fmt.Sprintf("%.1f", l.Pct)})
		}
	}
	cw.Flush()
}

func firstOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}
