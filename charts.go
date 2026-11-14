package main

import (
	"encoding/json"
	"net/http"
)

type apiAccount struct{ ID, Name string }
type apiCategory struct{ Name, Group string }

// apiTransactions returns every transaction in a compact form; the Charts page filters and
// aggregates in the browser so every control responds instantly.
func (a *app) apiTransactions(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(`SELECT t.date, t.amount, t.category, t.account_id, t.name
		FROM transactions t ORDER BY t.date LIMIT 200000`)
	if err != nil {
		fail(w, err, 500)
		return
	}
	defer rows.Close()
	txns := make([][]any, 0, 1024)
	for rows.Next() {
		var d, c, acct, n string
		var amt float64
		if err := rows.Scan(&d, &amt, &c, &acct, &n); err != nil {
			fail(w, err, 500)
			return
		}
		txns = append(txns, []any{d, amt, c, acct, n})
	}
	var accts []apiAccount
	ar, err := a.db.Query(`SELECT plaid_account_id, name || CASE WHEN mask<>'' THEN ' ••'||mask ELSE '' END FROM accounts ORDER BY name`)
	if err == nil {
		for ar.Next() {
			var x apiAccount
			ar.Scan(&x.ID, &x.Name)
			accts = append(accts, x)
		}
		ar.Close()
	}
	cats := make([]apiCategory, 0, len(taxonomy))
	for _, c := range taxonomy {
		cats = append(cats, apiCategory{c.Name, c.Group})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"txns": txns, "accounts": accts, "categories": cats,
		"groups": []string{gIncome, gEss, gDisc, gDebt, gRisk, gNeut},
	})
}

func (a *app) chartsPage(w http.ResponseWriter, r *http.Request) {
	a.tpl.ExecuteTemplate(w, "charts.html", nil)
}
