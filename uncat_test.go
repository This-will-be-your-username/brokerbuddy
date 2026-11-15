package main

import (
	"io"
	"strings"
	"testing"
)

func TestUncategorizedSpendingIsTrackedEverywhere(t *testing.T) {
	a := testApp(t)
	withAccount(a)
	addTxn(t, a, "i1", "2026-03-01", "EMPLOYER PAY", -3000, "Salary & Wages", 90, false, "builtin")
	addTxn(t, a, "g1", "2026-03-02", "ALDI 1", 500, "Groceries", 90, false, "builtin")
	addTxn(t, a, "u1", "2026-03-03", "MYSTERY SHOP", 400, "Uncategorized", 0, false, "")
	rep, err := a.buildReport(1)
	if err != nil {
		t.Fatal(err)
	}
	if rep.UncatN != 1 || rep.UncatM != 400 || rep.ExpensesM != 900 || rep.SurplusM != 2100 {
		t.Errorf("uncategorised spending must count: %+v", rep)
	}
	_, lines, _, _ := a.mortgageBaseline(1, false)
	var m float64
	for _, l := range lines {
		m += l.Monthly
	}
	if m != 900 {
		t.Errorf("mortgage baseline should match the dashboard: %v", m)
	}
	r := newClient(t, a).get("/")
	b, _ := io.ReadAll(r.Body)
	s := string(b)
	if !strings.Contains(s, "still uncategorized") || !strings.Contains(s, `href="/transactions?category=Uncategorized"`) {
		t.Error("dashboard should show uncategorized as a line and a notice")
	}
}
