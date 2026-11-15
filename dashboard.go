package main

import (
	"net/http"
	"sort"
	"strconv"
	"time"
)

type monthPoint struct {
	Month            string
	Income, Expenses float64
}

type dashCat struct {
	Category string
	Monthly  float64
	Pct      float64 // of spending
}

type dashData struct {
	Months    []string
	Income    []float64
	Expenses  []float64
	Net       []float64
	TopLabels []string
	TopValues []float64
}

// monthlySeries returns income and expenses per calendar month between from and to (inclusive).
// Income = credits in income categories; expenses = debits (net of refunds) in everything except
// income and neutral categories (transfers, savings). Uncategorized counts as an expense.
func (a *app) monthlySeries(from, to string) ([]monthPoint, error) {
	rows, err := a.db.Query(`SELECT substr(date,1,7), category, SUM(amount) FROM transactions
		WHERE date>=? AND date<=? GROUP BY 1,2 ORDER BY 1`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := map[string]*monthPoint{}
	for rows.Next() {
		var m, c string
		var s float64
		rows.Scan(&m, &c, &s)
		p := by[m]
		if p == nil {
			p = &monthPoint{Month: m}
			by[m] = p
		}
		switch g := groupOf[c]; {
		case g == gIncome:
			p.Income += -s
		case g == gNeut && c != "Uncategorized":
			// transfers / savings: not income or spending
		default:
			p.Expenses += s
		}
	}
	out := make([]monthPoint, 0, len(by))
	for _, p := range by {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Month < out[j].Month })
	return out, nil
}

func (a *app) dashboard(w http.ResponseWriter, r *http.Request) {
	months := 6
	if v := r.URL.Query().Get("months"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 120 {
			months = n
		}
	}
	span := months
	if months == 0 {
		span = 1200 // all time
	}
	rep, err := a.buildReport(span)
	if err != nil {
		fail(w, err, 500)
		return
	}
	data := dashData{}
	var recent []txnRow
	var topCats []dashCat
	if !rep.Empty {
		pts, err := a.monthlySeries(rep.From, rep.To)
		if err != nil {
			fail(w, err, 500)
			return
		}
		for _, p := range pts {
			data.Months = append(data.Months, p.Month)
			data.Income = append(data.Income, round2(p.Income))
			data.Expenses = append(data.Expenses, round2(p.Expenses))
			data.Net = append(data.Net, round2(p.Income-p.Expenses))
		}
		for _, g := range rep.Groups {
			if g.Name == gIncome || g.Name == gNeut {
				continue
			}
			for _, l := range g.Lines {
				topCats = append(topCats, dashCat{Category: l.Category, Monthly: l.Monthly})
			}
		}
		sort.Slice(topCats, func(i, j int) bool { return topCats[i].Monthly > topCats[j].Monthly })
		if len(topCats) > 8 {
			topCats = topCats[:8]
		}
		for i := range topCats {
			if rep.ExpensesM > 0 {
				topCats[i].Pct = topCats[i].Monthly / rep.ExpensesM * 100
			}
			data.TopLabels = append(data.TopLabels, topCats[i].Category)
			data.TopValues = append(data.TopValues, round2(topCats[i].Monthly))
		}
		rows, err := a.db.Query(`SELECT t.plaid_txn_id,t.date,t.name,a.name,t.category,t.amount,t.manual,t.cat_source
			FROM transactions t JOIN accounts a ON a.plaid_account_id=t.account_id ORDER BY t.date DESC, t.plaid_txn_id LIMIT 8`)
		if err == nil {
			for rows.Next() {
				var t txnRow
				rows.Scan(&t.ID, &t.Date, &t.Name, &t.Account, &t.Category, &t.Amount, &t.Manual, &t.Source)
				recent = append(recent, t)
			}
			rows.Close()
		}
	}
	savings := 0.0
	if rep.IncomeM > 0 {
		savings = rep.SurplusM / rep.IncomeM * 100
	}
	var uncat int
	a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE manual=0 AND (confidence<50 OR category='Uncategorized')`).Scan(&uncat)
	a.tpl.ExecuteTemplate(w, "dashboard.html", map[string]any{
		"Rep": rep, "Data": data, "Top": topCats, "Recent": recent, "Months": months,
		"SavingsRate": savings, "Uncat": uncat, "Flash": r.URL.Query().Get("msg"),
		"Updated": time.Now().Format("2 Jan 2006"),
	})
}

func round2(f float64) float64 { return float64(int64(f*100+sign(f)*0.5)) / 100 }

func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}
