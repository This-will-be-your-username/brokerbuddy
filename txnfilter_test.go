package main

import (
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func seedFilterData(t *testing.T) *app {
	a := testApp(t)
	a.db.Exec(`INSERT INTO accounts(plaid_account_id,item_id,name,mask,type) VALUES('a1',NULL,'Everyday','1234','manual')`)
	a.db.Exec(`INSERT INTO accounts(plaid_account_id,item_id,name,mask,type) VALUES('a2',NULL,'Savings','','manual')`)
	add := func(id, acct, date, name string, amt float64, cat string, conf int, manual int, src string) {
		if _, err := a.db.Exec(`INSERT INTO transactions(plaid_txn_id,account_id,date,name,amount,category,confidence,manual,cat_source) VALUES(?,?,?,?,?,?,?,?,?)`,
			id, acct, date, name, amt, cat, conf, manual, src); err != nil {
			t.Fatal(err)
		}
	}
	add("f1", "a1", "2026-01-05", "WOOLWORTHS 1", 80, "Groceries", 90, 0, "builtin")
	add("f2", "a1", "2026-02-10", "COLES 2", 120, "Groceries", 90, 0, "builtin")
	add("f3", "a1", "2026-02-15", "SALARY ACME", -3000, "Salary & Wages", 90, 0, "builtin")
	add("f4", "a2", "2026-03-01", "INTEREST PAID", -4.5, "Other Income", 90, 0, "builtin")
	add("f5", "a2", "2026-03-02", "NETFLIX", 22.99, "Subscriptions", 90, 0, "builtin")
	add("f6", "a1", "2026-03-03", "50% OFF SALE", 15, "Shopping & Clothing", 100, 1, "manual")
	add("f7", "a1", "2026-03-04", "ODD THING", 9, "Uncategorized", 0, 0, "")
	return a
}

func fids(html string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`name="id" value="(f\d+)"`).FindAllStringSubmatch(html, -1) {
		out = append(out, m[1])
	}
	return out
}

func list(t *testing.T, c *testClient, query string) (ids []string, html string) {
	t.Helper()
	b, _ := io.ReadAll(c.get("/transactions?" + query).Body)
	html = string(b)
	return fids(html), html
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, x := range a {
		m[x] = true
	}
	for _, y := range b {
		if !m[y] {
			return false
		}
	}
	return true
}

func TestFilters(t *testing.T) {
	a := seedFilterData(t)
	c := newClient(t, a)
	cases := []struct {
		name, query string
		want        []string
	}{
		{"no filter", "", []string{"f1", "f2", "f3", "f4", "f5", "f6", "f7"}},
		{"search", "q=coles", []string{"f2"}},
		{"search is literal (percent sign)", "q=" + url.QueryEscape("50%"), []string{"f6"}},
		{"search with underscore is literal", "q=" + url.QueryEscape("_"), nil},
		{"one account", "account=a2", []string{"f4", "f5"}},
		{"two accounts", "account=a1&account=a2", []string{"f1", "f2", "f3", "f4", "f5", "f6", "f7"}},
		{"one category", "category=Groceries", []string{"f1", "f2"}},
		{"two categories", "category=Groceries&category=Subscriptions", []string{"f1", "f2", "f5"}},
		{"category and account together", "category=Groceries&account=a2", nil},
		{"group", "group=Income", []string{"f3", "f4"}},
		{"group and category together", "group=Essential+living&category=Subscriptions", nil},
		{"money out", "dir=out", []string{"f1", "f2", "f5", "f6", "f7"}},
		{"money in", "dir=in", []string{"f3", "f4"}},
		{"from date", "from=2026-03-01", []string{"f4", "f5", "f6", "f7"}},
		{"to date", "to=2026-02-10", []string{"f1", "f2"}},
		{"date range", "from=2026-02-01&to=2026-03-01", []string{"f2", "f3", "f4"}},
		{"amount from (absolute value)", "min=100", []string{"f2", "f3"}},
		{"amount to", "max=10", []string{"f4", "f7"}},
		{"amount range", "min=15&max=100", []string{"f1", "f5", "f6"}},
		{"set by you", "how=manual", []string{"f6"}},
		{"built-in list", "how=builtin", []string{"f1", "f2", "f3", "f4", "f5"}},
		{"not matched", "how=none", []string{"f7"}},
		{"confidence + account", "confidence=review&account=a1", []string{"f7"}},
		{"everything combined", "q=o&account=a1&category=Groceries&dir=out&from=2026-01-01&min=50&max=100&confidence=high&how=builtin", []string{"f1"}},
	}
	for _, tc := range cases {
		got, _ := list(t, c, tc.query)
		if !sameSet(got, tc.want) {
			t.Errorf("%s (%s): got %v want %v", tc.name, tc.query, got, tc.want)
		}
	}
}

func TestFilterInputIsValidated(t *testing.T) {
	a := seedFilterData(t)
	c := newClient(t, a)
	evil := []string{
		"q=" + url.QueryEscape("' OR 1=1 --"),
		"account=" + url.QueryEscape("a1') OR ('1'='1"),
		"category=" + url.QueryEscape("Groceries' OR '1'='1"),
		"group=" + url.QueryEscape("x'; DROP TABLE transactions;--"),
		"from=" + url.QueryEscape("2026-01-01' OR 1=1"),
		"min=" + url.QueryEscape("0 OR 1=1"),
		"confidence=" + url.QueryEscape("high' OR 1=1"),
		"sort=" + url.QueryEscape("date; DROP TABLE transactions"),
		"how=" + url.QueryEscape("x' OR 1=1"),
		"page=" + url.QueryEscape("-5"),
	}
	for _, q := range evil {
		resp := c.get("/transactions?" + q)
		if resp.StatusCode != 200 {
			t.Errorf("%s: status %d", q, resp.StatusCode)
		}
	}
	var n int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM transactions`).Scan(&n); err != nil || n != 7 {
		t.Fatalf("data must be intact: %d %v", n, err)
	}
	// a bogus account id simply matches nothing; bad dates/amounts/categories are dropped, not applied
	if ids, _ := list(t, c, "account="+url.QueryEscape("a1') OR ('1'='1")); len(ids) != 0 {
		t.Errorf("a made-up account id must match nothing: %v", ids)
	}
	f := parseTxnFilter(url.Values{"from": {"nope"}, "min": {"-3"}, "max": {"abc"}, "category": {"Not A Category", "Groceries"}, "dir": {"sideways"}, "sort": {"zzz"}, "group": {"Nope"}})
	if f.From != "" || f.Min != "" || f.Max != "" || f.Dir != "" || f.Sort != "" || f.Group != "" || len(f.Cats) != 1 || f.Cats[0] != "Groceries" {
		t.Errorf("invalid values should be ignored: %+v", f)
	}
}

func TestSorting(t *testing.T) {
	a := seedFilterData(t)
	c := newClient(t, a)
	order := func(q string) string { ids, _ := list(t, c, q); return strings.Join(ids, ",") }
	if got := order(""); got != "f7,f6,f5,f4,f3,f2,f1" {
		t.Errorf("newest first: %s", got)
	}
	if got := order("sort=date_asc"); got != "f1,f2,f3,f4,f5,f6,f7" {
		t.Errorf("oldest first: %s", got)
	}
	if got := order("sort=amount_desc"); !strings.HasPrefix(got, "f3,f2,f1") {
		t.Errorf("largest first: %s", got)
	}
	if got := order("sort=amount_asc"); !strings.HasPrefix(got, "f4,f7") {
		t.Errorf("smallest first: %s", got)
	}
	if got := order("sort=name"); !strings.HasPrefix(got, "f6,f2,f4") { // "50%…", "COLES…", "INTEREST…"
		t.Errorf("A-Z: %s", got)
	}
}

func TestAccountColumnAndAccountLabels(t *testing.T) {
	a := seedFilterData(t)
	c := newClient(t, a)
	_, html := list(t, c, "")
	if !strings.Contains(html, "<th>Account</th>") {
		t.Error("the table needs an Account column")
	}
	if !strings.Contains(html, `title="Everyday ••1234"`) || !strings.Contains(html, `title="Savings"`) {
		t.Error("each row shows the account it belongs to (name and last digits)")
	}
}

func TestFilterMenuIsCollapsedAndShowsActiveFilters(t *testing.T) {
	a := seedFilterData(t)
	c := newClient(t, a)
	_, html := list(t, c, "")
	if !strings.Contains(html, `id="fpanel" class="fgrid" hidden`) || !strings.Contains(html, `aria-expanded="false"`) {
		t.Error("the filter menu must start collapsed")
	}
	if strings.Contains(html, `class="badge"`) {
		t.Error("no filters active, so no badge")
	}
	_, html = list(t, c, "account=a2&dir=out&min=5&q=net")
	if !strings.Contains(html, `class="badge" title="4 active"`) {
		t.Errorf("badge should count the 4 active filters")
	}
	for _, want := range []string{"Account: Savings", "Direction: Money out", "At least $5", "Search: “net”"} {
		if !strings.Contains(html, want) {
			t.Errorf("missing active-filter label %q", want)
		}
	}
	// each pill's link removes only that filter
	remove := regexp.MustCompile(`<a href="([^"]+)" title="Remove this filter" aria-label="Remove filter: Account: Savings"`).FindStringSubmatch(html)
	if remove == nil {
		t.Fatal("no remove link for the account filter")
	}
	href := strings.ReplaceAll(remove[1], "&amp;", "&")
	u, _ := url.Parse(href)
	q := u.Query()
	if q.Get("account") != "" || q.Get("dir") != "out" || q.Get("min") != "5" || q.Get("q") != "net" {
		t.Errorf("remove link should drop only the account: %s", href)
	}
}

func TestPagination(t *testing.T) {
	a := testApp(t)
	a.db.Exec(`INSERT INTO accounts(plaid_account_id,item_id,name,type) VALUES('a1',NULL,'Everyday','manual')`)
	tx, _ := a.db.Begin()
	for i := 0; i < 450; i++ {
		tx.Exec(`INSERT INTO transactions(plaid_txn_id,account_id,date,name,amount,category,confidence) VALUES(?,?,?,?,?,?,?)`,
			fmt.Sprintf("f%d", 1000+i), "a1", fmt.Sprintf("2026-%02d-%02d", 1+i/28%12, 1+i%28), fmt.Sprintf("ITEM %d", i), 10, "Groceries", 90)
	}
	tx.Commit()
	c := newClient(t, a)
	ids1, html1 := list(t, c, "")
	ids2, html2 := list(t, c, "page=2")
	ids3, html3 := list(t, c, "page=3")
	idsBig, _ := list(t, c, "page=99")
	if len(ids1) != 200 || len(ids2) != 200 || len(ids3) != 50 {
		t.Fatalf("page sizes %d %d %d", len(ids1), len(ids2), len(ids3))
	}
	if !sameSet(idsBig, ids3) {
		t.Error("a page past the end should show the last page")
	}
	for _, id := range ids2 {
		for _, id1 := range ids1 {
			if id == id1 {
				t.Fatalf("pages overlap at %s", id)
			}
		}
	}
	if !strings.Contains(html1, "Showing 1–200 of 450") || !strings.Contains(html3, "Showing 401–450 of 450") || !strings.Contains(html2, "Page 2 of 3") {
		t.Error("the range and page number should be shown")
	}
	// paging keeps the filters
	_, filtered := list(t, c, "q=ITEM&dir=out")
	m := regexp.MustCompile(`<a href="([^"]+)"><button type="button">Next`).FindStringSubmatch(filtered)
	if m == nil || !strings.Contains(m[1], "q=ITEM") || !strings.Contains(m[1], "dir=out") || !strings.Contains(m[1], "page=2") {
		t.Errorf("next link should keep the filters: %v", m)
	}
}

func TestSummaryTotalsFollowTheFilters(t *testing.T) {
	a := seedFilterData(t)
	c := newClient(t, a)
	_, html := list(t, c, "account=a1&dir=out")
	// f1 80 + f2 120 + f6 15 + f7 9 = 224.00 out, nothing in
	if !strings.Contains(html, "$224.00 out") || !strings.Contains(html, "$0.00 in") || !strings.Contains(html, "of 4 transactions") {
		t.Errorf("summary: %.0s", html)
	}
}

func TestFmtMoney(t *testing.T) {
	for in, want := range map[float64]string{0: "0.00", 5: "5.00", 999.5: "999.50", 1000: "1,000.00", 13475.2: "13,475.20", 1234567.891: "1,234,567.89", -2500: "-2,500.00"} {
		if got := fmtMoney(in); got != want {
			t.Errorf("%v: %q want %q", in, got, want)
		}
	}
}
