package main

import "testing"

func TestClassify(t *testing.T) {
	user := []rule{{"starbucks", "Work Expenses"}}
	plain := &classifier{}
	withUser := &classifier{rules: user}
	withCache := &classifier{cache: map[string]cacheEntry{"joes hardware": {Category: "Home & Strata"}}}
	cases := []struct {
		c              *classifier
		name, hint     string
		amt            float64
		wantCat, wantS string
	}{
		{plain, "EFTPOS WOOLWORTHS 1234 SYDNEY NSW", "", 54.2, "Groceries", "builtin"},
		{plain, "VISA PURCHASE COLES EXPRESS 887 PARRAMATTA", "", 60, "Transport & Fuel", "builtin"}, // longest phrase wins over "coles"
		{plain, "UBER EATS SYDNEY", "", 30, "Dining & Takeaway", "builtin"},                          // over "uber"
		{plain, "UBER *TRIP HELP.UBER.COM", "", 22, "Transport & Fuel", "builtin"},
		{plain, "STARBUCKS", "", 5, "Dining & Takeaway", "builtin"},
		{withUser, "STARBUCKS", "", 5, "Work Expenses", "rule"}, // user rule wins
		{plain, "SPORTSBET.COM.AU", "", 100, "Gambling", "builtin"},
		{plain, "AFTERPAY SYDNEY", "", 40, "Buy Now Pay Later", "builtin"},
		{plain, "ATM WITHDRAWAL CBA GEORGE ST", "", 200, "Cash Withdrawals", "builtin"},
		{plain, "ATM FEE", "", 2, "Fees & Dishonours", "builtin"},
		{plain, "DISHONOUR FEE", "", 15, "Fees & Dishonours", "builtin"},
		{plain, "Salary ACME PTY LTD", "", -2500, "Salary & Wages", "builtin"},
		{plain, "Salary refund", "", 50, "Uncategorized", ""}, // "salary" on a debit is not income
		{plain, "CENTRELINK FAMILY TAX", "", -300, "Government Benefits", "builtin"},
		{plain, "RAY WHITE RENT 12 SMITH ST", "", 600, "Rent", "builtin"},
		{plain, "RENT FROM TENANT", "", -500, "Rental Income", "builtin"},
		{plain, "Current account transfer", "", 20, "Transfers", "builtin"}, // "rent" must not match inside "current"
		{plain, "Interest paid", "", -1.2, "Other Income", "builtin"},
		{plain, "HOME LOAN REPAYMENT", "", 2000, "Mortgage & Loan Repayments", "builtin"},
		{plain, "WOOLWORTHS", "", -10, "Groceries", "builtin"},                             // refund keeps its spend category
		{withCache, "EFTPOS JOES HARDWARE 22 PENRITH", "", 80, "Home & Strata", "builtin"}, // dictionary "hardware" still hits first
		{plain, "Random Shop", "ENTERTAINMENT", 20, "Entertainment", "hint"},
		{plain, "Mystery", "", 5, "Uncategorized", ""},
		{plain, "Foo", "csv:Misc", 5, "Misc", "hint"},
	}
	for _, c := range cases {
		cat, src := c.c.classify(c.name, "", c.hint, c.amt)
		if cat != c.wantCat || src != c.wantS {
			t.Errorf("%q (%.0f): got %q/%q want %q/%q", c.name, c.amt, cat, src, c.wantCat, c.wantS)
		}
	}
}

func TestCacheUsedWhenDictionaryMisses(t *testing.T) {
	c := &classifier{cache: map[string]cacheEntry{"zorbas taverna": {Category: "Dining & Takeaway"}}}
	if cat, src := c.classify("EFTPOS ZORBAS TAVERNA 1234 NSW", "", "", 40); cat != "Dining & Takeaway" || src != "lookup" {
		t.Errorf("%q %q", cat, src)
	}
}

func TestMerchantKey(t *testing.T) {
	for in, want := range map[string]string{
		"EFTPOS PURCHASE BUNNINGS 1234 SYDNEY NSW":      "bunnings sydney",
		"Visa Purchase 12Jan ZORBAS TAVERNA PARRAMATTA": "zorbas taverna",
		"BPAY 12345": "",
	} {
		if got := merchantKey(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestKindToCategory(t *testing.T) {
	if kindToCategory("Australian supermarket chain") != "Groceries" || kindToCategory("sports betting company") != "Gambling" ||
		kindToCategory("a tree") != "" {
		t.Error("kind mapping")
	}
}

func TestTaxonomyCoversDictionary(t *testing.T) {
	for cat := range dictSrc {
		if _, ok := groupOf[cat]; !ok {
			t.Errorf("dictionary category %q missing from taxonomy", cat)
		}
	}
	for _, cat := range pfcMap {
		if _, ok := groupOf[cat]; !ok {
			t.Errorf("pfc category %q missing from taxonomy", cat)
		}
	}
}
