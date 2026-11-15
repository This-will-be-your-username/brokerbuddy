package main

import (
	"io"
	"net/url"
	"strings"
	"testing"
)

func reimbClassifier(max float64) *classifier {
	return &classifier{reimb: reimbCfg{enabled: true, max: max}}
}

func TestRepaymentsFromOthersAreFlaggedAndOffsetTheRightCategory(t *testing.T) {
	c := reimbClassifier(300)
	cases := []struct {
		name string
		amt  float64
		cat  string
		conf int
	}{
		{"OSKO PAYMENT FROM JANE SMITH dinner", -45, "Dining & Takeaway", 60},
		{"TRANSFER FROM TOM B swimming lessons", -80, "Education & Childcare", 60},
		{"PAYID FROM A LEE bus fare", -30, "Transport & Fuel", 60},
		{"FAST TRANSFER FROM KATE movie tickets", -25, "Entertainment", 60},
		{"OSKO PAYMENT FROM MIKE rent share", -150, "Rent", 60},
		{"TRANSFER FROM SAM birthday present", -40, "Gifts & Donations", 60},
		{"OSKO PAYMENT FROM LEE", -30, "Transfers", 35}, // no memo: held until you choose the category
		{"FAST TRANSFER FROM J CITIZEN", -12.5, "Transfers", 35},
	}
	for _, tc := range cases {
		r := c.classifyFull(tc.name, "", "", tc.amt)
		if !r.Reimb || r.Cat != tc.cat || r.Conf != tc.conf || r.Src != "reimb" {
			t.Errorf("%q: got %+v want reimb/%s/%d", tc.name, r, tc.cat, tc.conf)
		}
	}
}

func TestThingsThatAreNotRepayments(t *testing.T) {
	c := reimbClassifier(300)
	for _, tc := range []struct {
		name string
		amt  float64
		why  string
	}{
		{"OSKO PAYMENT FROM JANE SMITH dinner", 45, "a debit is never a repayment"},
		{"TRANSFER FROM JANE SMITH", -500, "bigger than the limit"},
		{"TRANSFER FROM SAVINGS", -50, "your own account"},
		{"OSKO PAYMENT FROM OWN ACCOUNT", -50, "your own account"},
		{"TRANSFER FROM CBA 1234", -50, "another of your banks"},
		{"SALARY ACME PTY LTD", -250, "salary"},
		{"CENTRELINK FAMILY TAX", -250, "benefits"},
		{"INTEREST PAID", -3, "interest"},
		{"REFUND KMART", -20, "a merchant refund"},
		{"MYSTERY DEPOSIT", -50, "no person-to-person wording"},
		{"TRANSFER FROM LOAN ACCOUNT", -50, "a loan"},
	} {
		if r := c.classifyFull(tc.name, "", "", tc.amt); r.Reimb {
			t.Errorf("%q must not be flagged (%s): %+v", tc.name, tc.why, r)
		}
	}
}

func TestReimbLimitAndSwitch(t *testing.T) {
	small := reimbClassifier(50)
	if r := small.classifyFull("OSKO PAYMENT FROM JANE dinner", "", "", -45); !r.Reimb {
		t.Error("$45 is within a $50 limit")
	}
	if r := small.classifyFull("OSKO PAYMENT FROM JANE dinner", "", "", -80); r.Reimb {
		t.Error("$80 is over a $50 limit")
	}
	off := &classifier{reimb: reimbCfg{enabled: false, max: 300}}
	if r := off.classifyFull("OSKO PAYMENT FROM JANE dinner", "", "", -45); r.Reimb {
		t.Error("automatic flagging is switched off")
	}
	// ...but your own choice still works when it is off
	if r := off.classifyFullWith("OSKO PAYMENT FROM JANE dinner", "", "", -45, 1); !r.Reimb || r.Cat != "Dining & Takeaway" {
		t.Errorf("forced: %+v", r)
	}
}

func TestPayerYouAssignedBeforeIsRemembered(t *testing.T) {
	c := reimbClassifier(300)
	c.learn = &learner{ex: []example{{toks: descriptorTokens("JANE SMITH"), cat: "Education & Childcare", order: 1}}}
	r := c.classifyFull("OSKO PAYMENT FROM JANE SMITH", "", "", -60) // no memo, but you categorised Jane before
	if !r.Reimb || r.Cat != "Education & Childcare" || r.Src != "learned" {
		t.Errorf("a payer you assigned earlier should follow: %+v", r)
	}
}

func TestOverrides(t *testing.T) {
	c := reimbClassifier(300)
	// the user says "this is a repayment" for something the wording would not catch
	if r := c.classifyFullWith("MYSTERY DEPOSIT", "", "", -50, 1); !r.Reimb || r.Cat != "Transfers" {
		t.Errorf("forced yes: %+v", r)
	}
	// ...or "this is not": an automatic match is dropped
	if r := c.classifyFullWith("OSKO PAYMENT FROM JANE dinner", "", "", -45, -1); r.Reimb {
		t.Errorf("forced no: %+v", r)
	}
	// a debit can never be flagged, even when forced
	if r := c.classifyFullWith("OSKO PAYMENT TO JANE", "", "", 45, 1); r.Reimb {
		t.Errorf("debit: %+v", r)
	}
}

// ---- stored flag, handlers, analysis ----

func seedReimb(t *testing.T) *app {
	a := testApp(t)
	withAccount(a)
	addTxn(t, a, "sal", "2026-01-02", "SALARY ACME", -3000, "Salary & Wages", 90, false, "builtin")
	addTxn(t, a, "r1", "2026-01-05", "RESTAURANT KOSTAS", 200, "Dining & Takeaway", 90, false, "builtin")
	addTxn(t, a, "g1", "2026-01-06", "WOOLWORTHS 1", 150, "Groceries", 90, false, "builtin")
	addTxn(t, a, "p1", "2026-01-08", "OSKO PAYMENT FROM JANE SMITH dinner", -60, "Uncategorized", 0, false, "")
	addTxn(t, a, "p2", "2026-01-09", "FAST TRANSFER FROM TOM B", -35, "Uncategorized", 0, false, "")
	addTxn(t, a, "own", "2026-01-10", "TRANSFER FROM SAVINGS", -500, "Uncategorized", 0, false, "")
	if _, err := recategorize(a.db); err != nil {
		t.Fatal(err)
	}
	return a
}

func reimbOf(a *app, id string) (flag int, cat string, conf int) {
	a.db.QueryRow(`SELECT reimb, category, confidence FROM transactions WHERE plaid_txn_id=?`, id).Scan(&flag, &cat, &conf)
	return
}

func TestRecategoriseFlagsRepaymentsAndStoresThem(t *testing.T) {
	a := seedReimb(t)
	if f, cat, _ := reimbOf(a, "p1"); f != 1 || cat != "Dining & Takeaway" {
		t.Errorf("p1: %d %s", f, cat)
	}
	if f, cat, conf := reimbOf(a, "p2"); f != 1 || cat != "Transfers" || conf != 35 {
		t.Errorf("p2 (no memo) is held: %d %s %d", f, cat, conf)
	}
	for _, id := range []string{"sal", "own", "r1", "g1"} {
		if f, _, _ := reimbOf(a, id); f != 0 {
			t.Errorf("%s must not be flagged", id)
		}
	}
	if n := a.unassignedReimb(); n != 1 {
		t.Errorf("one repayment is waiting for a category: %d", n)
	}
}

func TestRepaymentsReduceSpendingAndNeverCountAsIncome(t *testing.T) {
	a := seedReimb(t)
	rep, err := a.buildReport(1)
	if err != nil {
		t.Fatal(err)
	}
	line := func(cat string) float64 {
		for _, g := range rep.Groups {
			for _, l := range g.Lines {
				if l.Category == cat {
					return l.Total
				}
			}
		}
		return 0
	}
	if got := line("Dining & Takeaway"); got != 140 { // 200 spent - 60 repaid by Jane
		t.Errorf("dining after the repayment: %.2f want 140", got)
	}
	if rep.IncomeM < 2999 || rep.IncomeM > 3001 {
		t.Errorf("income must be just the salary (not the 60 and 35 credits): %.0f", rep.IncomeM)
	}
	if rep.ReimbN != 1 || rep.ReimbAmt != 60 || rep.ReimbHeld != 1 {
		t.Errorf("report totals: n=%d amt=%.0f held=%d", rep.ReimbN, rep.ReimbAmt, rep.ReimbHeld)
	}
	text := ""
	for _, f := range rep.Flags {
		text += f.Text + "\n"
	}
	if !strings.Contains(text, "reduced spending by $60.00") || !strings.Contains(text, "1 small credit(s) from other people have not been matched") {
		t.Errorf("the Lender view should say so:\n%s", text)
	}
	// the dashboard series agrees
	pts, _ := a.monthlySeries("2026-01-01", "2026-01-31")
	if len(pts) != 1 || pts[0].Income > 3001 || pts[0].Expenses != 200+150-60 {
		t.Errorf("dashboard: %+v", pts)
	}
	// the mortgage baseline too
	_, lines, _, _ := a.mortgageBaseline(1, false)
	for _, l := range lines {
		if l.Category == "Dining & Takeaway" && l.Monthly != 140 {
			t.Errorf("mortgage baseline dining %.0f", l.Monthly)
		}
	}
}

func TestAssigningTheHeldRepaymentOffsetsThatCategory(t *testing.T) {
	a := seedReimb(t)
	c := newClient(t, a)
	c.post("/txn/category", url.Values{"id": {"p2"}, "category": {"Groceries"}}) // Tom paid back groceries
	rep, _ := a.buildReport(1)
	var groceries float64
	for _, g := range rep.Groups {
		for _, l := range g.Lines {
			if l.Category == "Groceries" {
				groceries = l.Total
			}
		}
	}
	if groceries != 150-35 {
		t.Errorf("groceries after Tom's repayment: %.0f want 115", groceries)
	}
	if f, _, _ := reimbOf(a, "p2"); f != 1 {
		t.Error("assigning a category keeps it flagged as a repayment")
	}
	if n := a.unassignedReimb(); n != 0 {
		t.Errorf("nothing waiting any more: %d", n)
	}
}

func TestFlagAndUnflagByHand(t *testing.T) {
	a := seedReimb(t)
	c := newClient(t, a)
	// the savings transfer is rightly not flagged, but the user can still say a credit is a repayment
	a.db.Exec(`INSERT INTO transactions(plaid_txn_id,account_id,date,name,amount,category,confidence) VALUES('odd','acct','2026-01-11','DEPOSIT BY CASH',-40,'Other Income',20)`)
	if r := c.post("/txn/reimb", url.Values{"id": {"odd"}, "mode": {"yes"}}); r.StatusCode != 303 {
		t.Fatalf("flag: %d", r.StatusCode)
	}
	if f, cat, _ := reimbOf(a, "odd"); f != 1 || cat != "Transfers" {
		t.Errorf("flagged by hand (held): %d %s", f, cat)
	}
	// "not a repayment" on an automatic match reverts it to the ordinary classification
	c.post("/txn/reimb", url.Values{"id": {"p1"}, "mode": {"no"}})
	if f, cat, _ := reimbOf(a, "p1"); f != 0 || cat == "Dining & Takeaway" {
		t.Errorf("unflagged: %d %s", f, cat)
	}
	// and it stays unflagged through later recategorisation
	recategorize(a.db)
	if f, _, _ := reimbOf(a, "p1"); f != 0 {
		t.Error("your 'not a repayment' must stick")
	}
	// back to automatic
	c.post("/txn/reimb", url.Values{"id": {"p1"}, "mode": {"auto"}})
	if f, _, _ := reimbOf(a, "p1"); f != 1 {
		t.Error("automatic detection should flag it again")
	}
	// a debit can't be flagged
	if r := c.post("/txn/reimb", url.Values{"id": {"r1"}, "mode": {"yes"}}); r.StatusCode != 400 {
		t.Errorf("flagging a debit: %d", r.StatusCode)
	}
}

func TestListFilterBannerAndButtons(t *testing.T) {
	a := seedReimb(t)
	c := newClient(t, a)
	ids := func(q string) []string {
		b, _ := io.ReadAll(c.get("/transactions?" + q).Body)
		return rowIDs(string(b))
	}
	if got := ids("reimb=only"); !sameSet(got, []string{"p1", "p2"}) {
		t.Errorf("only: %v", got)
	}
	if got := ids("reimb=unassigned"); !sameSet(got, []string{"p2"}) {
		t.Errorf("unassigned: %v", got)
	}
	b, _ := io.ReadAll(c.get("/transactions").Body)
	html := string(b)
	for _, want := range []string{"small credit(s) from other people look like repayments", "/transactions?reimb=unassigned", "repaid by others", "repayment · pick category", "Not a repayment", "Repaid by others"} {
		if !strings.Contains(html, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	// the banner is not repeated on the list it links to
	b, _ = io.ReadAll(c.get("/transactions?reimb=unassigned").Body)
	if strings.Contains(string(b), "look like repayments but are not matched") {
		t.Error("no banner on the to-assign list")
	}
	if !strings.Contains(string(b), "Repayments to assign") {
		t.Error("active filter pill missing")
	}
}

func rowIDs(html string) []string { // every transaction id on the page, once
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(html, `name="id" value="`)[1:] {
		id := strings.SplitN(part, `"`, 2)[0]
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func TestSettingsSaveAndEffect(t *testing.T) {
	a := seedReimb(t)
	c := newClient(t, a)
	b, _ := io.ReadAll(c.get("/settings").Body)
	if !strings.Contains(string(b), "Repayments from other people") || !strings.Contains(string(b), `name="max" value="300"`) {
		t.Error("settings section missing")
	}
	// a smaller limit drops the $60 repayment (kept at $35)
	c.post("/settings/reimb", url.Values{"enabled": {"on"}, "max": {"40"}})
	if f, _, _ := reimbOf(a, "p1"); f != 0 {
		t.Error("$60 is now over the limit")
	}
	if f, _, _ := reimbOf(a, "p2"); f != 1 {
		t.Error("$35 is still within it")
	}
	// bad input is rejected and changes nothing
	r := c.post("/settings/reimb", url.Values{"enabled": {"on"}, "max": {"abc"}})
	if !strings.Contains(r.Header.Get("Location"), "err=") {
		t.Error("a non-number must be rejected")
	}
	if cfg := loadReimbCfg(a.db); cfg.max != 40 || !cfg.enabled {
		t.Errorf("settings changed by a rejected save: %+v", cfg)
	}
	// switching it off unflags the automatic ones but keeps what you flagged yourself
	c.post("/txn/reimb", url.Values{"id": {"p1"}, "mode": {"yes"}})
	c.post("/settings/reimb", url.Values{"max": {"300"}}) // enabled box unticked
	if f, _, _ := reimbOf(a, "p2"); f != 0 {
		t.Error("automatic flags go when it is switched off")
	}
	if f, _, _ := reimbOf(a, "p1"); f != 1 {
		t.Error("a flag you set by hand stays")
	}
}

func TestImportFlagsRepayments(t *testing.T) {
	a := testApp(t)
	c := newClient(t, a)
	csv := "Date,Description,Debit,Credit\n01/03/2026,RESTAURANT KOSTAS,120.00,\n02/03/2026,OSKO PAYMENT FROM JANE SMITH dinner,,40.00\n03/03/2026,FAST TRANSFER FROM TOM B,,25.00\n04/03/2026,SALARY ACME,,2000.00\n"
	_, tok := uploadMapping(t, c, csv)
	r := c.post("/import/run", url.Values{"token": {tok}, "date": {"0"}, "layout": {"1"}, "desc": {"1"}, "desc2": {"-1"}, "category": {"-1"}, "mode": {"split"},
		"amount": {"-1"}, "debit": {"2"}, "credit": {"3"}, "account": {"new"}, "new_name": {"Everyday"}, "action": {"import"}})
	loc, _ := url.QueryUnescape(r.Header.Get("Location"))
	if !strings.Contains(loc, "2 repayment(s) from other people") {
		t.Errorf("the import summary should mention them: %s", loc)
	}
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE reimb=1`).Scan(&n)
	if n != 2 {
		t.Errorf("two repayments flagged on import: %d", n)
	}
	var dining string
	a.db.QueryRow(`SELECT category FROM transactions WHERE name LIKE 'OSKO%'`).Scan(&dining)
	if dining != "Dining & Takeaway" {
		t.Errorf("Jane's dinner repayment offsets dining: %s", dining)
	}
}
