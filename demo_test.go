package main

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

var demoNow = time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

func TestDemoDataIsDeterministicAndFictional(t *testing.T) {
	a, b := demoTransactions(demoNow), demoTransactions(demoNow)
	if len(a) < 600 || len(a) != len(b) {
		t.Fatalf("expected a year of data, got %d / %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("same input must give the same data")
		}
	}
	// nothing that looks like contact details or a long account number
	bad := regexp.MustCompile(`@|\d{9,}`)
	for _, x := range a {
		if bad.MatchString(x.desc) && !strings.Contains(x.desc, "QANTAS") {
			t.Errorf("suspicious demo description %q", x.desc)
		}
	}
}

func TestLoadDemoGivesAUsefulDataset(t *testing.T) {
	a := testApp(t)
	n, err := loadDemo(a.db, demoNow)
	if err != nil || n < 600 {
		t.Fatalf("%d %v", n, err)
	}
	count := func(q string) (c int) { a.db.QueryRow(q).Scan(&c); return }
	total := count(`SELECT COUNT(*) FROM transactions`)
	unknown := count(`SELECT COUNT(*) FROM transactions WHERE category='Uncategorized' AND ignored=0`)
	if unknown == 0 || unknown*5 > total {
		t.Errorf("a few unknowns are wanted for the demo, not most: %d of %d", unknown, total)
	}
	if count(`SELECT COUNT(*) FROM transactions WHERE ignored=1`) == 0 {
		t.Error("card repayments should be ignored")
	}
	if count(`SELECT COUNT(*) FROM transactions WHERE reimb=1`) == 0 {
		t.Error("some repayments from friends expected")
	}
	for _, cat := range []string{"Salary & Wages", "Rent", "Groceries", "Dining & Takeaway", "Subscriptions", "Insurance", "Travel & Holidays"} {
		if count(`SELECT COUNT(*) FROM transactions WHERE category='`+cat+`'`) == 0 {
			t.Errorf("no %s in the demo data", cat)
		}
	}
	rep, err := a.buildReport(12)
	if err != nil || rep.IncomeM < 8000 || rep.ExpensesM < 4000 || rep.SurplusM <= 0 {
		t.Errorf("%+v %v", rep, err)
	}
	if _, err := loadDemo(a.db, demoNow); err == nil {
		t.Error("loading twice must be refused")
	}
}

func TestDemoNeverMixesWithRealData(t *testing.T) {
	a := testApp(t)
	withAccount(a)
	addTxn(t, a, "r1", "2026-03-01", "REAL PURCHASE", 10, "Groceries", 90, false, "builtin")
	if _, err := loadDemo(a.db, demoNow); err == nil {
		t.Fatal("demo must not load alongside real transactions")
	}
	b := testApp(t)
	loadDemo(b.db, demoNow)
	if err := removeDemo(b.db); err != nil || demoExists(b.db) {
		t.Fatal("remove failed")
	}
	var n int
	b.db.QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&n)
	if n != 0 {
		t.Errorf("transactions left: %d", n)
	}
	// removing demo data leaves real data alone
	addTxn(t, a, "r2", "2026-03-02", "ANOTHER", 5, "Groceries", 90, false, "builtin")
	removeDemo(a.db)
	a.db.QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&n)
	if n != 2 {
		t.Errorf("real data must survive: %d", n)
	}
}

func TestDemoButtonsWork(t *testing.T) {
	a := testApp(t)
	c := newClient(t, a)
	body := func(path string) string {
		r := c.get(path)
		buf := make([]byte, 1<<20)
		n, _ := r.Body.Read(buf)
		return string(buf[:n])
	}
	if !strings.Contains(body("/"), "Load demo data") {
		t.Error("empty dashboard should offer the demo")
	}
	c.post("/demo/load", url.Values{})
	if !demoExists(a.db) || strings.Contains(body("/"), "Load demo data") {
		t.Error("demo should be loaded")
	}
	if !strings.Contains(body("/settings"), "Remove demo data") {
		t.Error("settings should offer removal")
	}
	c.post("/demo/remove", url.Values{})
	if demoExists(a.db) {
		t.Error("demo should be gone")
	}
}
