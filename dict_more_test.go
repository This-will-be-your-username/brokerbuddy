package main

import "testing"

// Examples are anonymised stand-ins for the kinds of lines found when profiling a real year of
// statements: the description wording, not the particular shops, is what the rules generalise.
func TestNewRulesCategoriseCommonUnknowns(t *testing.T) {
	c := &classifier{}
	cases := []struct {
		desc string
		amt  float64
		cat  string
		min  int // minimum confidence
	}{
		// billing platforms and national brands
		{"Acme Fit Training - Direct Debit - Receipt 107722 EZYPAY", 50, "Health & Medical", 85},
		{"Ezypay*Studio Fitness Sydney AUS", 30, "Health & Medical", 65},
		{"gu health rewards GR SUBURBTON AUS", 160, "Insurance", 85},
		{"LEAPTEL SUBURBTON AUS", 80, "Utilities & Internet", 85},
		{"SPRIGGY SCHOOLS SYDNEY AUS", 45, "Education & Childcare", 85},
		{"Small Animal Clinic Suburbton AUS", 900, "Pets", 85},
		{"UNITED1621150119831 CONTINENTAL AUS", 1500, "Travel & Holidays", 80},
		{"HIPCAMP BOOKING SOMEWHERE AUS", 300, "Travel & Holidays", 85},
		{"AIRALO SINGAPORE SGP", 18, "Travel & Holidays", 85},
		{"YOU TECHNOLOGIES GROUP - Visa Purchase - Receipt 100", 1000, "Travel & Holidays", 85},
		{"Grab* A-9VAJI45GX7ISAV - Visa Purchase - Receipt 14712", 22, "Transport & Fuel", 65},
		{"PAYPAL *PADDLE.NET 35314369001 GBR", 17, "Subscriptions", 80},
		{"WW METRO 1012 NTH SUBURB AUS", 12, "Groceries", 85},
		{"ANTHROPIC* SUBSCRIPTION SAN FRANCISCO", 30, "Subscriptions", 60},
		// words that identify a type of business (a little less certain than a brand)
		{"ZLR*Deli Hub - Some St Suburb AUS", 12, "Groceries", 60},
		{"SOME TOWN PRIMEMEAT SUBURBTON", 40, "Groceries", 50}, // a run-together name ending in a business word
		{"ANZ CARDS 4564680119916814 - BPAY Bill Payment - To ANZ CARDS", 900, "Credit Card Repayments", 85},
		{"Crowne Plaza Seaside Seaside AUS", 400, "Travel & Holidays", 85},
		{"WOOLIESMOBILE SUBURBTON AUS", 40, "Utilities & Internet", 85},
		{"TRANSPORT FOR NS   -  ETOLL4489362794", 25, "Transport & Fuel", 85},
		{"ARABICA - Visa Purchase - Receipt 192835", 6, "Dining & Takeaway", 80},
		{"SMP*Henris Patisserie Suburb AUS", 9, "Dining & Takeaway", 60},
		{"LSP*Mrs Smith the Bake Suburb AUS", 9, "Dining & Takeaway", 60},
		{"SQ *CLAY GREEN BAKE Suburb AUS", 9, "Dining & Takeaway", 60},
		{"SPICY TACO FUSION SUBURB AUS", 40, "Dining & Takeaway", 60},
		{"TGTG ab12cd34ef56 Sydney AUS", 6, "Dining & Takeaway", 80},
		{"GYG SUBURBTON Suburbton AUS", 14, "Dining & Takeaway", 65},
		{"SOMEWHERE SUBURB PHYSIOTHERAPY PTY SUBURB AUS", 120, "Health & Medical", 60},
		{"TOMS CELLARS SUBURB AUS", 60, "Alcohol & Tobacco", 60},
		{"SMP*Wine House Hunter Valley AUS", 60, "Alcohol & Tobacco", 60},
		{"Suburbton Swimming - Direct Debit - Receipt 17682", 34, "Education & Childcare", 60},
		{"RAGING WATERS SUBURBTON AUS", 60, "Entertainment", 85},
		{"The Pet Connection Suburb AUS", 40, "Pets", 60},
		{"Australia Post - Receipt 6846", 12, "Shopping & Clothing", 60},
		{"EXAMPLE HOTEL SUBURBTON AUS", 45, "Dining & Takeaway", 60},
		// payment channel wording is not part of who was paid
		{"PAYPAL AUSTRALIA - Direct Debit - Receipt 139006 105", 12, "Shopping & Clothing", 55},
	}
	for _, tc := range cases {
		r := c.classifyFull(tc.desc, "", "", tc.amt)
		if tc.min == 0 { // a documented limitation: must NOT be confidently categorised
			if r.Conf >= 50 {
				t.Errorf("%q should be left for review, got %+v", tc.desc, r)
			}
			continue
		}
		if r.Cat != tc.cat || r.Conf < tc.min {
			t.Errorf("%q: got %s (%s, %d) want %s (>= %d)", tc.desc, r.Cat, r.Src, r.Conf, tc.cat, tc.min)
		}
	}
}

func TestPayPalIsOnlyALowConfidenceGuess(t *testing.T) {
	r := (&classifier{}).classifyFull("PAYPAL AUSTRALIA - Direct Debit - Receipt 139006", "", "", 30)
	if r.Conf != 55 || confLevel(r.Conf, false, r.Cat) != "medium" {
		t.Errorf("PayPal could be anything: %+v", r)
	}
}

// "<some merchant> CRED VOUCHER <the real payee>": the first merchant is not who was paid, so it must not
// become a "known merchant" that other transactions at that shop are matched to.
func TestVoucherLinesDoNotTeachTheWrongMerchant(t *testing.T) {
	voucher := "SOME BUTCHER SUBURB AUS CRED VOUCHER AMAZON AU MARKETPLACE SYDNEY"
	if got := descriptorTokens(voucher); len(got) == 0 || got[0] != "amazon" {
		t.Errorf("the payee is what follows the marker: %v", got)
	}
	c := &classifier{hist: hist(voucher, "Shopping & Clothing", 90)}
	if r := c.classifyFull("SOME BUTCHER SUBURB AUS", "", "", 40); r.Cat == "Shopping & Clothing" {
		t.Errorf("a plain purchase at that butcher must not become Shopping: %+v", r)
	}
	if r := c.classifyFull(voucher, "", "", 40); r.Cat != "Shopping & Clothing" {
		t.Errorf("the voucher line itself is Amazon: %+v", r)
	}
}

func TestFuzzyHistoryGuessesAreAlwaysLowConfidence(t *testing.T) {
	c := &classifier{hist: hist("ALPHA BETA GAMMA", "Dining & Takeaway", 90)}
	r := c.classifyFull("ALPHA BETA DELTA", "", "", 10)
	if r.Src != "history" || r.Conf >= 50 {
		t.Errorf("a look-alike is a guess to review: %+v", r)
	}
	// an identical name is stronger evidence
	r = c.classifyFull("ALPHA BETA GAMMA", "", "", 10)
	if r.Conf != 70 {
		t.Errorf("identical name: %+v", r)
	}
}

func TestNewDictionaryCategoriesExist(t *testing.T) {
	for cat := range dictMore {
		if _, ok := groupOf[cat]; !ok {
			t.Errorf("dictMore category %q is not in the taxonomy", cat)
		}
	}
}

func TestDictionaryIsDeterministic(t *testing.T) {
	for i := 1; i < len(dict); i++ { // longest first; ties broken the same way every run
		a, b := dict[i-1], dict[i]
		if len(a.pat) < len(b.pat) || (len(a.pat) == len(b.pat) && (a.pat > b.pat || (a.pat == b.pat && a.cat > b.cat))) {
			t.Fatalf("dictionary order broken at %q / %q", a.pat, b.pat)
		}
	}
}

func TestSuffixGuessDoesNotOverreach(t *testing.T) {
	c := &classifier{}
	for _, d := range []string{"INCIDENTAL EXPENSES", "WHEAT FARM PTY", "ACCIDENTAL DAMAGE", "FACETIME CAFETERIA"} {
		if r := c.classifyFull(d, "", "", 20); r.Src == "fuzzy" {
			t.Errorf("%q should not be guessed from a word ending: %+v", d, r)
		}
	}
	if cat, ok := suffixGuess([]string{"primemeat"}); !ok || cat != "Groceries" {
		t.Errorf("%q %v", cat, ok)
	}
}

func TestAppleComPurchasesAreCategorised(t *testing.T) {
	r := (&classifier{}).classifyFull("APPLE.COM/AU Sydney AUS", "", "", 30)
	if r.Cat != "Shopping & Clothing" || r.Conf < 60 {
		t.Errorf("%+v", r)
	}
	// the recurring-bill wording still wins
	if r := (&classifier{}).classifyFull("APPLE.COM/BILL Sydney AUS", "", "", 30); r.Cat != "Subscriptions" {
		t.Errorf("%+v", r)
	}
}
