package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestFlipDirectionReversesAndRecomputes(t *testing.T) {
	a := testApp(t)
	withAccount(a)
	addTxn(t, a, "x1", "2026-03-01", "ACME STUDIO SUBURBTON", -205, "Uncategorized", 0, false, "") // recorded as a credit, really a debit
	addTxn(t, a, "m1", "2026-03-02", "SOMETHING", -10, "Groceries", 100, true, "manual")
	c := newClient(t, a)
	get := func(id string) (amt float64, flipped int, cat string) {
		a.db.QueryRow(`SELECT amount, flipped, category FROM transactions WHERE plaid_txn_id=?`, id).Scan(&amt, &flipped, &cat)
		return
	}
	c.post("/txn/flip", url.Values{"id": {"x1"}})
	if amt, f, _ := get("x1"); amt != 205 || f != 1 {
		t.Errorf("flipped: %v %d", amt, f)
	}
	c.post("/txn/flip", url.Values{"id": {"m1"}})
	if amt, _, cat := get("m1"); amt != 10 || cat != "Groceries" {
		t.Errorf("a manual category survives: %v %s", amt, cat)
	}
	c.post("/txn/flip", url.Values{"id": {"x1"}}) // and back
	if amt, f, _ := get("x1"); amt != -205 || f != 0 {
		t.Errorf("flip twice restores: %v %d", amt, f)
	}
	if r := c.post("/txn/flip", url.Values{"id": {"nope"}}); r.StatusCode != 404 {
		t.Errorf("unknown id: %d", r.StatusCode)
	}
}

func TestFlipAffectsReportsAndPageShowsControl(t *testing.T) {
	a := testApp(t)
	withAccount(a)
	addTxn(t, a, "i1", "2026-03-01", "EMPLOYER PAY", -3000, "Salary & Wages", 90, false, "builtin")
	addTxn(t, a, "g1", "2026-03-02", "ALDI 1", -500, "Groceries", 90, false, "builtin") // a debit exported as a credit
	c := newClient(t, a)
	before, _ := a.buildReport(1)
	c.post("/txn/flip", url.Values{"id": {"g1"}})
	after, _ := a.buildReport(1)
	if before.ExpensesM >= after.ExpensesM || after.ExpensesM != 500 {
		t.Errorf("expenses before %v after %v", before.ExpensesM, after.ExpensesM)
	}
	r := c.get("/transactions")
	buf := make([]byte, 1<<20)
	n, _ := r.Body.Read(buf)
	s := string(buf[:n])
	if !strings.Contains(s, `action="/txn/flip"`) || !strings.Contains(s, "reversed") {
		t.Error("list should offer the control and mark reversed rows")
	}
}

func TestReimportAfterFlipDoesNotDuplicate(t *testing.T) {
	a := testApp(t)
	c := newClient(t, a)
	a.db.Exec(`INSERT INTO accounts(plaid_account_id,item_id,name,type) VALUES('acct',NULL,'Everyday','manual')`)
	csv := "Date,Description,Debit,Credit\n01/03/2026,ACME STUDIO,,205.00\n"
	form := func() url.Values {
		_, tok := uploadMapping(t, c, csv)
		return url.Values{"token": {tok}, "date": {"0"}, "layout": {"1"}, "desc": {"1"}, "desc2": {"-1"}, "category": {"-1"}, "mode": {"split"},
			"amount": {"-1"}, "debit": {"2"}, "credit": {"3"}, "account": {"acct"}, "action": {"import"}}
	}
	c.post("/import/run", form())
	var id string
	a.db.QueryRow(`SELECT plaid_txn_id FROM transactions`).Scan(&id)
	c.post("/txn/flip", url.Values{"id": {id}})
	c.post("/import/run", form())
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&n)
	if n != 1 {
		t.Errorf("re-import duplicated a corrected row: %d", n)
	}
}
