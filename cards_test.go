package main

import (
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestIsCardRepayment(t *testing.T) {
	yes := []string{"PAYMENT RECEIVED - THANK YOU", "BPAY PAYMENT", "Payment - Thank You", "DIRECT DEBIT PAYMENT", "AUTOPAY", "Internet Banking Transfer", "CARD REPAYMENT"}
	no := []string{"REFUND KMART", "PAYMENT REFUND", "REWARDS CASHBACK", "INTEREST ADJUSTMENT", "WOOLWORTHS", "CHARGEBACK DISPUTE"}
	for _, d := range yes {
		if !isCardRepayment(d, -300) {
			t.Errorf("%q (a credit) should be a repayment", d)
		}
		if isCardRepayment(d, 300) {
			t.Errorf("%q as a purchase (debit) must not be a repayment", d)
		}
	}
	for _, d := range no {
		if isCardRepayment(d, -20) {
			t.Errorf("%q should not be treated as a repayment", d)
		}
	}
}

func seedCard(t *testing.T) *app {
	a := testApp(t)
	a.db.Exec(`INSERT INTO accounts(plaid_account_id,item_id,name,type,is_card) VALUES('bank',NULL,'Everyday','manual',0)`)
	a.db.Exec(`INSERT INTO accounts(plaid_account_id,item_id,name,type,is_card) VALUES('card',NULL,'Rewards Card','manual',0)`)
	add := func(id, acct, date, name string, amt float64, cat string) {
		if _, err := a.db.Exec(`INSERT INTO transactions(plaid_txn_id,account_id,date,name,amount,category,confidence) VALUES(?,?,?,?,?,?,90)`, id, acct, date, name, amt, cat); err != nil {
			t.Fatal(err)
		}
	}
	add("b-sal", "bank", "2026-01-02", "SALARY ACME", -3000, "Salary & Wages")
	add("b-pay", "bank", "2026-01-20", "CREDIT CARD PAYMENT", 300, "Credit Card Repayments")
	add("b-food", "bank", "2026-01-05", "ALDI 5", 60, "Groceries")
	add("c-buy1", "card", "2026-01-06", "COLES 1", 50, "Groceries")
	add("c-buy2", "card", "2026-01-09", "UBER TRIP", 20, "Transport & Fuel")
	add("c-pay", "card", "2026-01-21", "PAYMENT RECEIVED - THANK YOU", -300, "Other Income") // what an unknown credit looks like
	add("c-ref", "card", "2026-01-12", "REFUND KMART", -15, "Shopping & Clothing")
	return a
}

func ignoredIDs(a *app) map[string]bool {
	out := map[string]bool{}
	rows, _ := a.db.Query(`SELECT plaid_txn_id FROM transactions WHERE ignored=1`)
	defer rows.Close()
	for rows.Next() {
		var id string
		rows.Scan(&id)
		out[id] = true
	}
	return out
}

func TestNothingIsIgnoredUntilAnAccountIsFlaggedAsACard(t *testing.T) {
	a := seedCard(t)
	if n, err := recomputeIgnored(a.db); err != nil || n != 0 {
		t.Fatalf("no card flagged: %d %v", n, err)
	}
}

func TestFlaggingACardIgnoresRepaymentsOnBothSides(t *testing.T) {
	a := seedCard(t)
	c := newClient(t, a)
	r := c.post("/accounts/flag", url.Values{"id": {"card"}, "card": {"on"}})
	if r.StatusCode != 303 || !strings.Contains(r.Header.Get("Location"), "msg=") {
		t.Fatalf("flag: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	ign := ignoredIDs(a)
	if !ign["c-pay"] {
		t.Error("the payment into the card must be ignored")
	}
	if !ign["b-pay"] {
		t.Error("the matching payment from the bank account must be ignored (the card purchases are counted instead)")
	}
	for _, id := range []string{"c-buy1", "c-buy2", "c-ref", "b-sal", "b-food"} {
		if ign[id] {
			t.Errorf("%s must still count (purchases, refunds and ordinary rows are not repayments)", id)
		}
	}
	// unflagging brings everything back
	c.post("/accounts/flag", url.Values{"id": {"card"}})
	if got := ignoredIDs(a); len(got) != 0 {
		t.Errorf("unflagged: %v", got)
	}
}

func TestSwitchingOffThePaidElsewhereRule(t *testing.T) {
	a := seedCard(t)
	c := newClient(t, a)
	c.post("/accounts/flag", url.Values{"id": {"card"}, "card": {"on"}})
	c.post("/settings/cards", url.Values{}) // checkbox unticked = off
	ign := ignoredIDs(a)
	if !ign["c-pay"] || ign["b-pay"] {
		t.Errorf("only the card-side repayment should be ignored now: %v", ign)
	}
	c.post("/settings/cards", url.Values{"elsewhere": {"on"}})
	if !ignoredIDs(a)["b-pay"] {
		t.Error("switching it back on ignores the bank-side payment again")
	}
}

func TestIgnoredRowsAreLeftOutOfEveryAnalysis(t *testing.T) {
	a := seedCard(t)
	before, _ := a.buildReport(1)
	// the card's "payment received" credit would count as extra income, the repayment as a debt
	if before.IncomeM < 3299 {
		t.Fatalf("setup: income should include the 300 credit: %.0f", before.IncomeM)
	}
	c := newClient(t, a)
	c.post("/accounts/flag", url.Values{"id": {"card"}, "card": {"on"}})
	after, _ := a.buildReport(1)
	if after.IncomeM > 3001 || after.IncomeM < 2999 {
		t.Errorf("income should be just the salary: %.0f", after.IncomeM)
	}
	debt := func(r *report) float64 {
		for _, g := range r.Groups {
			if g.Name == gDebt {
				return g.Total
			}
		}
		return 0
	}
	if debt(before) != 300 || debt(after) != 0 {
		t.Errorf("the repayment must stop counting as a debt: before %.0f after %.0f", debt(before), debt(after))
	}
	// the mortgage baseline uses the same data
	income, lines, _, _ := a.mortgageBaseline(1, false)
	if income > 3001 {
		t.Errorf("mortgage income %.0f", income)
	}
	for _, l := range lines {
		if l.Category == "Credit Card Repayments" {
			t.Errorf("repayments must not appear as spending: %+v", l)
		}
	}
	// charts API
	b, _ := io.ReadAll(c.get("/api/transactions").Body)
	if strings.Contains(string(b), "THANK YOU") || strings.Contains(string(b), "CREDIT CARD PAYMENT") {
		t.Error("the charts data must not include ignored rows")
	}
	// dashboard monthly series
	pts, _ := a.monthlySeries("2026-01-01", "2026-01-31")
	if len(pts) != 1 || pts[0].Income > 3001 {
		t.Errorf("dashboard series: %+v", pts)
	}
}

func TestCountItAnywayOverride(t *testing.T) {
	a := seedCard(t)
	c := newClient(t, a)
	c.post("/accounts/flag", url.Values{"id": {"card"}, "card": {"on"}})
	c.post("/txn/ignore", url.Values{"id": {"c-pay"}, "mode": {"include"}})
	if ignoredIDs(a)["c-pay"] {
		t.Error("an overridden row must count")
	}
	// ...and the override survives recomputation (e.g. after another import)
	recomputeIgnored(a.db)
	if ignoredIDs(a)["c-pay"] {
		t.Error("the override must stick")
	}
	b, _ := io.ReadAll(c.get("/transactions?ignored=show").Body)
	if !strings.Contains(string(b), "counted anyway") {
		t.Error("the row should say it is counted anyway")
	}
	c.post("/txn/ignore", url.Values{"id": {"c-pay"}, "mode": {"auto"}})
	if !ignoredIDs(a)["c-pay"] {
		t.Error("going back to automatic ignores it again")
	}
}

func TestTransactionsPageHidesAndShowsIgnoredRows(t *testing.T) {
	a := seedCard(t)
	c := newClient(t, a)
	c.post("/accounts/flag", url.Values{"id": {"card"}, "card": {"on"}})
	ids := func(q string) map[string]bool {
		b, _ := io.ReadAll(c.get("/transactions?" + q).Body)
		out := map[string]bool{}
		for _, m := range regexp.MustCompile(`name="id" value="([^"]+)"`).FindAllStringSubmatch(string(b), -1) {
			out[m[1]] = true
		}
		return out
	}
	def := ids("")
	if def["c-pay"] || def["b-pay"] || !def["c-buy1"] {
		t.Errorf("default view hides ignored rows: %v", def)
	}
	b, _ := io.ReadAll(c.get("/transactions").Body)
	if !strings.Contains(string(b), "2 ignored credit card repayment(s) hidden") {
		t.Error("the page must say how many are hidden, with a link to show them")
	}
	show := ids("ignored=show")
	if !show["c-pay"] || !show["b-pay"] || !show["c-buy1"] {
		t.Errorf("show: %v", show)
	}
	only := ids("ignored=only")
	if len(only) != 2 || !only["c-pay"] || !only["b-pay"] {
		t.Errorf("only: %v", only)
	}
	b, _ = io.ReadAll(c.get("/transactions?ignored=only").Body)
	if !strings.Contains(string(b), "Count it") || !strings.Contains(string(b), `class="pill" title="A payment into your credit card`) {
		t.Error("ignored rows show why and offer 'Count it'")
	}
}

func TestSettingsListsAccountsWithCardToggles(t *testing.T) {
	a := seedCard(t)
	c := newClient(t, a)
	b, _ := io.ReadAll(c.get("/settings").Body)
	html := string(b)
	if !strings.Contains(html, "Credit cards") || !strings.Contains(html, `aria-label="Rewards Card is a credit card"`) || !strings.Contains(html, `aria-label="Everyday is a credit card"`) {
		t.Error("every account needs a credit card toggle")
	}
	c.post("/accounts/flag", url.Values{"id": {"card"}, "card": {"on"}})
	b, _ = io.ReadAll(c.get("/settings").Body)
	if !strings.Contains(string(b), "1 repayment(s) ignored") || !strings.Contains(string(b), "1 card payment(s) ignored") {
		t.Errorf("per-account ignored counts missing")
	}
	if r := c.post("/accounts/flag", url.Values{"id": {"nope"}, "card": {"on"}}); r.StatusCode != 400 {
		t.Errorf("unknown account: %d", r.StatusCode)
	}
}

// ---- import ----

func cardCSV() string {
	return "Date,Description,Amount\n05/02/2026,COLES 22,54.20\n06/02/2026,UBER *TRIP,18.00\n10/02/2026,PAYMENT RECEIVED - THANK YOU,-300.00\n11/02/2026,REFUND KMART,-25.00\n"
}

func uploadMapping(t *testing.T, c *testClient, csv string) (html, tok string) {
	t.Helper()
	resp := multipartUpload(t, c, "/import/upload", "file", "card.csv", []byte(csv), nil)
	b, _ := io.ReadAll(resp.Body)
	html = string(b)
	m := regexp.MustCompile(`name="token" value="([a-f0-9]{32})"`).FindStringSubmatch(html)
	if m == nil {
		t.Fatalf("no token: %.400s", html)
	}
	return html, m[1]
}

func TestCardStatementIsRecognisedAndRepaymentsIgnoredOnImport(t *testing.T) {
	a := testApp(t)
	c := newClient(t, a)
	html, tok := uploadMapping(t, c, cardCSV())
	if !strings.Contains(html, "This looks like a credit card statement") || !regexp.MustCompile(`name="is_card"[^>]*checked`).MatchString(html) {
		t.Errorf("a card statement should be recognised and the tickbox pre-ticked")
	}
	form := url.Values{"token": {tok}, "date": {"0"}, "layout": {"1"}, "desc": {"1"}, "desc2": {"-1"}, "category": {"-1"}, "mode": {"single"}, "amount": {"2"},
		"invert": {"on"}, "debit": {"-1"}, "credit": {"-1"}, "account": {"new"}, "new_name": {"Visa"}, "is_card": {"on"}, "action": {"import"}}
	r := c.post("/import/run", form)
	loc, _ := url.QueryUnescape(r.Header.Get("Location"))
	if !strings.Contains(loc, "Imported 4 transactions") || !strings.Contains(loc, "1 credit card repayment(s) ignored") {
		t.Errorf("import message: %s", loc)
	}
	var card int
	a.db.QueryRow(`SELECT is_card FROM accounts WHERE name='Visa'`).Scan(&card)
	if card != 1 {
		t.Error("the account must be flagged as a credit card")
	}
	ign := ignoredIDs(a)
	if len(ign) != 1 {
		t.Fatalf("exactly the payment should be ignored: %v", ign)
	}
	var name string
	a.db.QueryRow(`SELECT name FROM transactions WHERE ignored=1`).Scan(&name)
	if !strings.Contains(name, "THANK YOU") {
		t.Errorf("wrong row ignored: %s", name)
	}
	// the refund is not a repayment and still counts
	var refund int
	a.db.QueryRow(`SELECT ignored FROM transactions WHERE name LIKE 'REFUND%'`).Scan(&refund)
	if refund != 0 {
		t.Error("refunds must keep counting")
	}
}

func TestUntickedCardBoxMeansNothingIsIgnored(t *testing.T) {
	a := testApp(t)
	c := newClient(t, a)
	_, tok := uploadMapping(t, c, cardCSV())
	form := url.Values{"token": {tok}, "date": {"0"}, "layout": {"1"}, "desc": {"1"}, "desc2": {"-1"}, "category": {"-1"}, "mode": {"single"}, "amount": {"2"},
		"invert": {"on"}, "debit": {"-1"}, "credit": {"-1"}, "account": {"new"}, "new_name": {"Plain account"}, "action": {"import"}}
	c.post("/import/run", form)
	if n := a.countIgnored(""); n != 0 {
		t.Errorf("a normal account keeps its credits: %d ignored", n)
	}
}

func TestExistingAccountFlagShownAndUpdatedOnImport(t *testing.T) {
	a := seedCard(t)
	c := newClient(t, a)
	a.db.Exec(`UPDATE accounts SET is_card=1 WHERE plaid_account_id='card'`)
	html, _ := uploadMapping(t, c, "Date,Description,Debit,Credit\n01/03/2026,COFFEE,5.00,\n")
	if !strings.Contains(html, `data-card="1"`) || !strings.Contains(html, "Rewards Card (credit card)") {
		t.Error("the account list should show which accounts are credit cards")
	}
	if strings.Contains(html, "looks like a credit card statement") {
		t.Error("an ordinary statement must not be flagged as a card")
	}
}

func TestPDFCardStatementEndToEnd(t *testing.T) {
	a := testApp(t)
	c := newClient(t, a)
	data, err := os.ReadFile(filepath.Join("testdata", "pdf", "card_c.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	resp := multipartUpload(t, c, "/import/upload", "file", "card.pdf", data, nil)
	b, _ := io.ReadAll(resp.Body)
	html := string(b)
	if !strings.Contains(html, "This looks like a credit card statement") {
		t.Fatalf("the PDF card statement should be recognised: %.300s", html)
	}
	tok := regexp.MustCompile(`name="token" value="([a-f0-9]{32})"`).FindStringSubmatch(html)[1]
	r := c.post("/import/run", url.Values{"token": {tok}, "date": {"0"}, "layout": {"0"}, "desc": {"1"}, "desc2": {"-1"}, "category": {"-1"}, "mode": {"single"}, "amount": {"2"},
		"invert": {"on"}, "debit": {"-1"}, "credit": {"-1"}, "account": {"new"}, "new_name": {"Rewards Card"}, "is_card": {"on"}, "action": {"import"}})
	loc, _ := url.QueryUnescape(r.Header.Get("Location"))
	if !strings.Contains(loc, "Imported 24 transactions") || !strings.Contains(loc, "credit card repayment(s) ignored") {
		t.Errorf("import: %s", loc)
	}
	_, exp := loadFixture(t, "card_c")
	want := 0
	for _, e := range exp {
		if e.Credit > 0 { // the payments received
			want++
		}
	}
	var n, other int
	a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE ignored=1 AND name LIKE 'PAYMENT RECEIVED%'`).Scan(&n)
	a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE ignored=1 AND name NOT LIKE 'PAYMENT RECEIVED%'`).Scan(&other)
	if want == 0 || n != want || other != 0 {
		t.Errorf("the fixture has %d payments received: %d ignored, %d other rows wrongly ignored", want, n, other)
	}
}
