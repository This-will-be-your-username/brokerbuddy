package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func hist(pairs ...any) *history { // (name, category, conf)...
	h := &history{}
	for i := 0; i+2 < len(pairs); i += 3 {
		toks := descriptorTokens(pairs[i].(string))
		h.ex = append(h.ex, histEx{toks, strings.Join(toks, " "), pairs[i+1].(string), pairs[i+2].(int)})
	}
	return h
}

func TestConfidenceLadder(t *testing.T) {
	rules := []rule{{"mycorp", "Salary & Wages"}}
	c := &classifier{rules: rules, hist: hist("ZORBAS TAVERNA", "Dining & Takeaway", 90)}
	cases := []struct {
		name       string
		hint       string
		amt        float64
		cat, src   string
		minC, maxC int
		level      string
	}{
		{"MYCORP PAY", "", -3000, "Salary & Wages", "rule", 95, 95, "high"},
		{"EFTPOS WOOLWORTHS METRO 12 SYDNEY", "", 40, "Groceries", "builtin", 85, 90, "high"},
		{"THE LITTLE CAFE", "", 9, "Dining & Takeaway", "builtin", 60, 70, "medium"},        // an ordinary word, less certain than a brand
		{"BP 5566 PARRAMATTA", "", 70, "Transport & Fuel", "builtin", 70, 75, "medium"},     // very short phrase
		{"EFTPOS WOOLWORHTS CITY", "", 40, "Groceries", "fuzzy", 50, 55, "medium"},          // typo of a known brand
		{"ZORBAS TAVERNA KINGSFORD", "", 40, "Dining & Takeaway", "history", 40, 45, "low"}, // a look-alike is only a guess: kept in the low band
		{"ZORBAS TAVERNA", "", 40, "Dining & Takeaway", "history", 70, 70, "medium"},        // identical name in history
		{"SOMETHING ODD", "ENTERTAINMENT", 20, "Entertainment", "hint", 50, 55, "medium"},   // the bank's own category
		{"MONTHLY ACCOUNT SERVICING CHARGE", "", 12, "Fees & Dishonours", "cue", 40, 40, "low"},
		{"MYSTERY DEPOSIT 99", "", -250, "Other Income", "cue", 20, 20, "low"}, // money in, no other clue
		{"QWZX PLORB", "", 30, "Uncategorized", "", 0, 0, "unknown"},           // nothing to go on, but still attempted
	}
	for _, tc := range cases {
		r := c.classifyFull(tc.name, "", tc.hint, tc.amt)
		if r.Cat != tc.cat || r.Src != tc.src || r.Conf < tc.minC || r.Conf > tc.maxC {
			t.Errorf("%q: got %s/%s/%d, want %s/%s/%d-%d", tc.name, r.Cat, r.Src, r.Conf, tc.cat, tc.src, tc.minC, tc.maxC)
		}
		if lv := confLevel(r.Conf, false, r.Cat); lv != tc.level {
			t.Errorf("%q: level %s want %s", tc.name, lv, tc.level)
		}
	}
}

func TestConfLevelBoundaries(t *testing.T) {
	for _, c := range []struct {
		conf   int
		manual bool
		cat    string
		want   string
	}{{100, true, "Groceries", "confirmed"}, {50, true, "Groceries", "confirmed"}, {90, false, "Groceries", "high"}, {80, false, "Groceries", "high"},
		{79, false, "Groceries", "medium"}, {50, false, "Groceries", "medium"}, {49, false, "Groceries", "low"}, {1, false, "Groceries", "low"},
		{0, false, "Groceries", "unknown"}, {70, false, "Uncategorized", "unknown"}} {
		if got := confLevel(c.conf, c.manual, c.cat); got != c.want {
			t.Errorf("%+v: %s", c, got)
		}
	}
}

func TestHistoryNeverMakesDebitsIncome(t *testing.T) {
	c := &classifier{hist: hist("ACME PAYMENTS", "Salary & Wages", 90)}
	if r := c.classifyFull("ACME PAYMENTS LTD", "", "", 50); r.Cat == "Salary & Wages" {
		t.Errorf("a debit must not become income via history: %+v", r)
	}
}

func TestSimilarity(t *testing.T) {
	if s := similarity([]string{"zorbas", "taverna"}, []string{"zorbas", "taverna"}); s != 1 {
		t.Errorf("identical: %v", s)
	}
	if s := similarity([]string{"zorbas", "taverna"}, []string{"zorba", "taverna"}); s < 0.9 {
		t.Errorf("typo: %v", s)
	}
	if s := similarity([]string{"bunnings"}, []string{"kmart"}); s != 0 {
		t.Errorf("unrelated: %v", s)
	}
}

// ---- stored confidence, filters, propagation, confirmation ----

func newFormRequest(c *testClient, path string, form url.Values) (*http.Request, error) {
	req, err := http.NewRequest("POST", c.srv.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", c.srv.URL)
	return req, nil
}

func addTxn(t *testing.T, a *app, id, date, name string, amt float64, cat string, conf int, manual bool, src string) {
	t.Helper()
	m := 0
	if manual {
		m = 1
	}
	if _, err := a.db.Exec(`INSERT INTO transactions(plaid_txn_id,account_id,date,name,amount,category,confidence,manual,cat_source) VALUES(?,?,?,?,?,?,?,?,?)`,
		id, "acct", date, name, amt, cat, conf, m, src); err != nil {
		t.Fatal(err)
	}
}

func withAccount(a *app) {
	a.db.Exec(`INSERT INTO accounts(plaid_account_id,item_id,name,type) VALUES('acct',NULL,'Test','manual')`)
}

func seedConf(t *testing.T) *app {
	a := testApp(t)
	withAccount(a)
	addTxn(t, a, "t-conf", "2026-01-01", "MANUALLY SET", 10, "Groceries", 100, true, "manual")
	addTxn(t, a, "t-high", "2026-01-02", "KNOWN BRAND", 10, "Groceries", 90, false, "builtin")
	addTxn(t, a, "t-med", "2026-01-03", "SO SO", 10, "Dining & Takeaway", 66, false, "builtin")
	addTxn(t, a, "t-low", "2026-01-04", "WEAK GUESS", 10, "Fees & Dishonours", 40, false, "cue")
	addTxn(t, a, "t-unk", "2026-01-05", "NO IDEA", 10, "Uncategorized", 0, false, "")
	return a
}

func idsOn(html string) []string {
	var out []string
	for _, id := range []string{"t-conf", "t-high", "t-med", "t-low", "t-unk"} {
		if strings.Contains(html, `value="`+id+`"`) {
			out = append(out, id)
		}
	}
	return out
}

func TestTransactionsFilterByConfidence(t *testing.T) {
	a := seedConf(t)
	c := newClient(t, a)
	want := map[string][]string{
		"":          {"t-conf", "t-high", "t-med", "t-low", "t-unk"},
		"confirmed": {"t-conf"},
		"high":      {"t-high"},
		"medium":    {"t-med"},
		"low":       {"t-low"},
		"unknown":   {"t-unk"},
		"review":    {"t-low", "t-unk"},
	}
	for f, exp := range want {
		b, _ := io.ReadAll(c.get("/transactions?confidence=" + f).Body)
		got := idsOn(string(b))
		if len(got) != len(exp) {
			t.Errorf("filter %q: got %v want %v", f, got, exp)
			continue
		}
		set := map[string]bool{}
		for _, g := range got {
			set[g] = true
		}
		for _, e := range exp {
			if !set[e] {
				t.Errorf("filter %q: got %v want %v", f, got, exp)
			}
		}
	}
	// chips carry the counts
	b, _ := io.ReadAll(c.get("/transactions").Body)
	html := string(b)
	for _, w := range []string{"Needs review <b>2</b>", "Unknown <b>1</b>", "Low <b>1</b>", "Medium <b>1</b>", "High <b>1</b>", "Confirmed <b>1</b>", "All <b>5</b>"} {
		if !strings.Contains(html, w) {
			t.Errorf("missing chip %q", w)
		}
	}
	// the review list puts the least sure first
	b, _ = io.ReadAll(c.get("/transactions?confidence=review").Body)
	if strings.Index(string(b), `value="t-unk"`) > strings.Index(string(b), `value="t-low"`) {
		t.Error("unknown (0) must come before low (40)")
	}
}

func TestSettingCategoryFixesOtherTransactionsFromTheSameMerchant(t *testing.T) {
	a := testApp(t)
	withAccount(a)
	// three visits to one unknown merchant (different store numbers and suburbs), one other merchant,
	// and one visit of the same merchant that you already set yourself
	addTxn(t, a, "m1", "2026-02-01", "EFTPOS ACME WIDGETS 123 SYDNEY NSW", 40, "Uncategorized", 0, false, "")
	addTxn(t, a, "m2", "2026-02-05", "EFTPOS ACME WIDGETS 456 PARRAMATTA NSW", 25, "Uncategorized", 0, false, "")
	addTxn(t, a, "m3", "2026-02-09", "VISA PURCHASE ACME WIDGETS 789 RYDE", 12, "Fees & Dishonours", 40, false, "cue")
	addTxn(t, a, "other", "2026-02-02", "EFTPOS QWZX PLORB 1", 9, "Uncategorized", 0, false, "")
	addTxn(t, a, "keep", "2026-02-03", "EFTPOS ACME WIDGETS 999 MANLY", 8, "Pets", 100, true, "manual")
	c := newClient(t, a)

	r := c.post("/txn/category", url.Values{"id": {"m1"}, "category": {"Shopping & Clothing"}})
	if r.StatusCode != 303 {
		t.Fatalf("status %d", r.StatusCode)
	}
	get := func(id string) (cat string, conf int, manual int, src string) {
		a.db.QueryRow(`SELECT category, confidence, manual, cat_source FROM transactions WHERE plaid_txn_id=?`, id).Scan(&cat, &conf, &manual, &src)
		return
	}
	if cat, conf, man, _ := get("m1"); cat != "Shopping & Clothing" || conf != 100 || man != 1 {
		t.Errorf("edited row: %s %d %d", cat, conf, man)
	}
	for _, id := range []string{"m2", "m3"} {
		if cat, conf, man, src := get(id); cat != "Shopping & Clothing" || conf < 85 || man != 0 || src != "learned" {
			t.Errorf("%s should follow the same merchant: %s conf=%d manual=%d src=%s", id, cat, conf, man, src)
		}
	}
	if cat, _, _, _ := get("other"); cat != "Uncategorized" {
		t.Errorf("a different merchant must be untouched: %s", cat)
	}
	if cat, _, man, _ := get("keep"); cat != "Pets" || man != 1 {
		t.Errorf("a transaction you set yourself must be left alone: %s", cat)
	}
}

func TestFlashSaysHowManyWereFixed(t *testing.T) {
	a := testApp(t)
	withAccount(a)
	addTxn(t, a, "m1", "2026-02-01", "ACME WIDGETS 1", 40, "Uncategorized", 0, false, "")
	addTxn(t, a, "m2", "2026-02-02", "ACME WIDGETS 2", 40, "Uncategorized", 0, false, "")
	c := newClient(t, a)
	req, _ := newFormRequest(c, "/txn/category", url.Values{"id": {"m1"}, "category": {"Shopping & Clothing"}})
	req.Header.Set("Referer", c.srv.URL+"/transactions?confidence=review")
	resp, _ := c.http.Do(req)
	loc, _ := url.QueryUnescape(resp.Header.Get("Location"))
	if !strings.Contains(loc, "/transactions?") || !strings.Contains(loc, "confidence=review") ||
		!strings.Contains(loc, "to 1 other") || !strings.Contains(loc, "1 of them were unsure") {
		t.Errorf("flash/redirect: %s", loc)
	}
}

func TestConfirmingAGuessMakesItConfirmedAndTeachesTheApp(t *testing.T) {
	a := testApp(t)
	withAccount(a)
	addTxn(t, a, "g1", "2026-02-01", "EFTPOS ZORBAS TAVERNA 12", 40, "Dining & Takeaway", 52, false, "fuzzy")
	addTxn(t, a, "g2", "2026-02-09", "EFTPOS ZORBAS TAVERNA 99", 35, "Uncategorized", 0, false, "")
	c := newClient(t, a)
	// the tick button posts the category the row already has
	c.post("/txn/category", url.Values{"id": {"g1"}, "category": {"Dining & Takeaway"}})
	var conf, manual int
	a.db.QueryRow(`SELECT confidence, manual FROM transactions WHERE plaid_txn_id='g1'`).Scan(&conf, &manual)
	if conf != 100 || manual != 1 {
		t.Errorf("confirmed: conf=%d manual=%d", conf, manual)
	}
	var cat string
	a.db.QueryRow(`SELECT category FROM transactions WHERE plaid_txn_id='g2'`).Scan(&cat)
	if cat != "Dining & Takeaway" {
		t.Errorf("the same merchant should now follow: %s", cat)
	}
}

func TestRecategorizeStoresConfidenceAndNeverUsesGuessesAsEvidence(t *testing.T) {
	a := testApp(t)
	withAccount(a)
	addTxn(t, a, "a", "2026-03-01", "EFTPOS WOOLWORTHS 1 SYDNEY", 50, "Uncategorized", 0, false, "")
	addTxn(t, a, "b", "2026-03-02", "EFTPOS WOOLWORHTS 2 SYDNEY", 50, "Uncategorized", 0, false, "") // typo
	addTxn(t, a, "c", "2026-03-03", "QWZX PLORB 3", 50, "Uncategorized", 0, false, "")
	if _, err := recategorize(a.db); err != nil {
		t.Fatal(err)
	}
	read := func(id string) (string, int, string) {
		var c, s string
		var n int
		a.db.QueryRow(`SELECT category, confidence, cat_source FROM transactions WHERE plaid_txn_id=?`, id).Scan(&c, &n, &s)
		return c, n, s
	}
	if c, n, s := read("a"); c != "Groceries" || n < 80 || s != "builtin" {
		t.Errorf("a: %s %d %s", c, n, s)
	}
	if c, n, s := read("b"); c != "Groceries" || n < 50 || n > 55 || s != "fuzzy" {
		t.Errorf("b (typo): %s %d %s", c, n, s)
	}
	if c, n, _ := read("c"); c != "Uncategorized" || n != 0 {
		t.Errorf("c: %s %d", c, n)
	}
	// a guess (fuzzy) is not evidence: a look-alike of "b" must not be categorised from b via history
	addTxn(t, a, "d", "2026-03-04", "PLORBX QWZXY 4", 50, "Uncategorized", 0, false, "")
	addTxn(t, a, "e", "2026-03-05", "PLORBX QWZXY 5", 50, "Dining & Takeaway", 52, false, "history")
	recategorize(a.db)
	if _, _, s := read("d"); s == "history" {
		t.Errorf("history must not be built from earlier guesses")
	}
}

func TestImportSummaryAndRedirect(t *testing.T) {
	a := testApp(t)
	c := newClient(t, a)
	n := 0
	importCSV := func(csv string) string {
		n++
		resp := multipartUpload(t, c, "/import/upload", "file", "x.csv", []byte(csv), nil)
		b, _ := io.ReadAll(resp.Body)
		html := string(b)
		if !strings.Contains(html, "Predicted categories") || !strings.Contains(html, "Predicted category") {
			t.Fatalf("mapping page must show predicted categories and confidence:\n%.800s", html)
		}
		tok := strings.Split(strings.Split(html, `name="token" value="`)[1], `"`)[0]
		r := c.post("/import/run", url.Values{"token": {tok}, "date": {"0"}, "layout": {"1"}, "desc": {"1"}, "desc2": {"-1"}, "category": {"-1"},
			"mode": {"split"}, "amount": {"-1"}, "debit": {"2"}, "credit": {"3"}, "account": {"new"}, "new_name": {fmt.Sprintf("Account %d", n)}, "action": {"import"}})
		loc, _ := url.QueryUnescape(r.Header.Get("Location"))
		return loc
	}
	good := importCSV("Date,Description,Debit,Credit\n01/02/2026,EFTPOS WOOLWORTHS 12 SYDNEY NSW,50.00,\n02/02/2026,SPORTSBET.COM.AU,20.00,\n")
	if !strings.HasPrefix(good, "/?msg=") || !strings.Contains(good, "good confidence") {
		t.Errorf("all-known import should land on the dashboard: %s", good)
	}
	mixed := importCSV("Date,Description,Debit,Credit\n03/02/2026,QWZX PLORB 77,15.00,\n04/02/2026,EFTPOS ALDI 5,30.00,\n05/02/2026,ANOTHER ODD THING,9.00,\n")
	if !strings.HasPrefix(mixed, "/transactions?confidence=review&msg=") || !strings.Contains(mixed, "1 high") || !strings.Contains(mixed, "2 low confidence") {
		t.Errorf("an import with doubtful rows should go straight to the review list: %s", mixed)
	}
	var zeroConfKnown int
	a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE category<>'Uncategorized' AND confidence=0`).Scan(&zeroConfKnown)
	if zeroConfKnown != 0 {
		t.Errorf("%d categorised rows have no confidence", zeroConfKnown)
	}
}
