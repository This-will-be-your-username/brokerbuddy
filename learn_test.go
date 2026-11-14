package main

import (
	"reflect"
	"testing"
)

func lr(pairs ...string) *classifier {
	l := &learner{}
	for i := 0; i+1 < len(pairs); i += 2 {
		l.ex = append(l.ex, example{descriptorTokens(pairs[i]), pairs[i+1], i})
	}
	return &classifier{learn: l}
}

func TestDescriptorTokens(t *testing.T) {
	for in, want := range map[string][]string{
		"EFTPOS PURCHASE BUNNINGS 7788 PENRITH NSW":      {"bunnings"},
		"COLES EXPRESS 887 PARRAMATTA":                   {"coles", "express"},
		"EFTPOS AESOP NEWTOWN NSW":                       {"aesop"},
		"7 ELEVEN 1234 SYDNEY":                           {"eleven"},
		"VISA PURCHASE ZORBAS TAVERNA PARRAMATTA NSW AU": {"zorbas", "taverna"},
	} {
		if got := descriptorTokens(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}

func TestTokEq(t *testing.T) {
	yes := [][2]string{{"zorbas", "zorba"}, {"woolworths", "woolworth"}, {"mcdonalds", "mcdonlds"}, {"bunnings", "bunnings"}}
	no := [][2]string{{"coles", "cole"}, {"target", "tarpet9"}, {"kmart", "kart"}, {"hardware", "software"}}
	for _, p := range yes {
		if !tokEq(p[0], p[1]) {
			t.Errorf("%v should match", p)
		}
	}
	for _, p := range no {
		if tokEq(p[0], p[1]) {
			t.Errorf("%v should not match", p)
		}
	}
}

func TestLearnedBeatsDictionaryForSameMerchant(t *testing.T) {
	c := lr("EFTPOS BUNNINGS 7788 PENRITH NSW", "Business Expense")
	// different store, same brand: strong match overrides the dictionary's Home & Strata
	if cat, src := c.classify("VISA PURCHASE BUNNINGS 1111 RYDE NSW", "", "", 80); cat != "Business Expense" || src != "learned" {
		t.Errorf("%q %q", cat, src)
	}
}

func TestFuzzyTypoMatch(t *testing.T) {
	c := lr("ZORBAS TAVERNA 12 NSW", "Dining & Takeaway")
	if cat, src := c.classify("ZORBA TAVERNA 99 NSW", "", "", 40); cat != "Dining & Takeaway" || src != "learned" {
		t.Errorf("%q %q", cat, src)
	}
}

func TestWeakMatchDoesNotOverrideDictionary(t *testing.T) {
	c := lr("COLES EXPRESS 887 PARRAMATTA", "Transport & Fuel")
	// plain Coles is a supermarket: example "coles express" only weakly matches, dictionary wins
	if cat, src := c.classify("COLES 456 SYDNEY NSW", "", "", 60); cat != "Groceries" || src != "builtin" {
		t.Errorf("%q %q", cat, src)
	}
}

func TestWeakMatchUsedWhenDictionaryHasNoAnswer(t *testing.T) {
	c := lr("ZED STORE EXTRA 5", "Shopping & Clothing")
	if cat, src := c.classify("ZED STORE 77", "", "", 10); cat != "Shopping & Clothing" || src != "learned" {
		t.Errorf("%q %q", cat, src)
	}
}

func TestConflictingExamplesDontGuess(t *testing.T) {
	c := lr("ZED STORE EXTRA 5", "Shopping & Clothing", "ZED CAFE ONE 6", "Dining & Takeaway")
	if cat, _ := c.classify("ZED 77", "", "", 10); cat != "Uncategorized" {
		t.Errorf("got %q", cat)
	}
}

func TestMostSpecificExampleWins(t *testing.T) {
	c := lr("ZED 1", "Shopping & Clothing", "ZED CAFE 2", "Dining & Takeaway")
	if cat, _ := c.classify("ZED CAFE BONDI 33", "", "", 10); cat != "Dining & Takeaway" {
		t.Errorf("got %q", cat)
	}
}

func TestLearnedIncomeNotAppliedToDebits(t *testing.T) {
	c := lr("ACME PAYMENTS", "Salary & Wages")
	if cat, _ := c.classify("ACME PAYMENTS REFUND", "", "", 50); cat == "Salary & Wages" {
		t.Error("a debit must never become income")
	}
	if cat, src := c.classify("ACME PAYMENTS", "", "", -3000); cat != "Salary & Wages" || src != "learned" {
		t.Errorf("%q %q", cat, src)
	}
}

func TestUserRuleStillBeatsLearned(t *testing.T) {
	c := lr("BUNNINGS 1", "Business Expense")
	c.rules = []rule{{"bunnings", "Home & Strata"}}
	if cat, src := c.classify("BUNNINGS 2", "", "", 10); cat != "Home & Strata" || src != "rule" {
		t.Errorf("%q %q", cat, src)
	}
}
