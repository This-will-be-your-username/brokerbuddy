package main

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

type sugResp struct {
	Merchant string
	Options  []suggestion
	Searched bool
	Note     string
	Error    string
}

func postSuggest(t *testing.T, c *testClient, id string, web bool) sugResp {
	t.Helper()
	f := url.Values{"id": {id}}
	if web {
		f.Set("web", "1")
	}
	r := c.post("/api/suggest", f)
	var out sugResp
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSuggestionsComeFromYourOwnHistory(t *testing.T) {
	a := testApp(t)
	withAccount(a)
	addTxn(t, a, "h1", "2026-01-01", "Zorbatron Cafe North", 12, "Dining & Takeaway", 100, true, "manual")
	addTxn(t, a, "h2", "2026-01-02", "Zorbatron Cafe South", 14, "Dining & Takeaway", 100, true, "manual")
	addTxn(t, a, "u1", "2026-02-01", "ZORBATRON SOMETHING", 20, "Uncategorized", 0, false, "")
	c := newClient(t, a)
	got := postSuggest(t, c, "u1", false)
	if len(got.Options) == 0 || got.Options[0].Category != "Dining & Takeaway" {
		t.Fatalf("%+v", got)
	}
	if s := got.Options[0].Score; s < 40 || s > 100 {
		t.Errorf("score %d", s)
	}
	if got.Searched {
		t.Error("no online search unless asked")
	}
}

func TestSuggestionsNeverEmptyAndSortedByScore(t *testing.T) {
	a := testApp(t)
	withAccount(a)
	addTxn(t, a, "g1", "2026-01-01", "ALDI 1", 50, "Groceries", 90, false, "builtin")
	addTxn(t, a, "u1", "2026-02-01", "QQQ WWW", 20, "Uncategorized", 0, false, "")
	got := postSuggest(t, newClient(t, a), "u1", false)
	if len(got.Options) == 0 {
		t.Fatal("expected fallback suggestions")
	}
	for i := 1; i < len(got.Options); i++ {
		if got.Options[i].Score > got.Options[i-1].Score {
			t.Errorf("not sorted: %+v", got.Options)
		}
	}
}

func TestOnlineSearchSendsOnlyTheMerchantNameAndIsCached(t *testing.T) {
	a := testApp(t)
	withAccount(a)
	addTxn(t, a, "u1", "2026-02-01", "ZORBATRON EXAMPLE - Visa Purchase - Receipt 99", 123.45, "Uncategorized", 0, false, "")
	var queried []string
	old := webSearch
	webSearch = func(key string) ([]suggestion, error) {
		queried = append(queried, key)
		return []suggestion{{"Pets", 58, "Wikipedia: Zorbatron — pet store chain", "web"}}, nil
	}
	defer func() { webSearch = old }()
	c := newClient(t, a)
	got := postSuggest(t, c, "u1", true)
	if len(got.Options) == 0 || got.Options[0].Category != "Pets" || !got.Searched {
		t.Fatalf("%+v", got)
	}
	postSuggest(t, c, "u1", true)
	if len(queried) != 1 {
		t.Fatalf("second request should use the cache: %v", queried)
	}
	if k := queried[0]; strings.ContainsAny(k, "0123456789") || strings.Contains(k, "receipt") || strings.Contains(k, "visa") {
		t.Errorf("only the cleaned merchant name may leave the machine, got %q", k)
	}
}

func TestSuggestApplyUsesExistingCategoryEndpoint(t *testing.T) {
	a := testApp(t)
	withAccount(a)
	addTxn(t, a, "u1", "2026-02-01", "ZORBATRON ONE", 20, "Uncategorized", 0, false, "")
	addTxn(t, a, "u2", "2026-02-02", "ZORBATRON ONE", 25, "Uncategorized", 0, false, "")
	c := newClient(t, a)
	c.post("/txn/category", url.Values{"id": {"u1"}, "category": {"Pets"}})
	var cat string
	a.db.QueryRow(`SELECT category FROM transactions WHERE plaid_txn_id='u2'`).Scan(&cat)
	if cat != "Pets" {
		t.Errorf("same merchant follows: %s", cat)
	}
}

func TestKindCandidatesFindsSeveral(t *testing.T) {
	got := kindCandidates("Australian pet store and online retailer")
	if len(got) < 2 {
		t.Errorf("%v", got)
	}
}

func TestSuggestButtonShownOnlyForDoubtfulRows(t *testing.T) {
	a := seedConf(t)
	body := new(strings.Builder)
	r := newClient(t, a).get("/transactions")
	buf := make([]byte, 1<<20)
	n, _ := r.Body.Read(buf)
	body.Write(buf[:n])
	if got := strings.Count(body.String(), `class="small-btn suggest-btn"`); got != 2 { // low + unknown
		t.Errorf("suggest buttons: %d", got)
	}
}
