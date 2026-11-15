package main

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestCleanTextRemovesHowItWasPaid(t *testing.T) {
	for in, want := range map[string]string{
		"WOOLWORTHS 1234 SYDNEY AUS - Visa Purchase - Receipt 147114":                     "woolworths 1234 sydney aus",
		"PAYPAL AUSTRALIA - Direct Debit - Receipt 139006 105 something":                  "paypal australia something",
		"Acme Fit Trainin - Direct Debit - Receipt 107722 EZYPAY":                         "acme fit trainin ezypay",
		"ARABICA - Visa Purchase - Receipt 192835Foreign Currency Amount: USD 4.50":       "arabica",
		"SHOP 1 - Visa Purchase - Receipt 1 Copyright © 2026 Example Banking Corporation": "shop 1",
		"SOMEWHERE Copyright © 2026 Westpac Banking Corporation ABN 33 007 457 141":       "somewhere",
		"PLAIN MERCHANT NAME":          "plain merchant name",
		"PAYMENT RECEIVED - THANK YOU": "payment received thank you",
	} {
		if got := norm(cleanText(in)); got != want {
			t.Errorf("%q\n got  %q\n want %q", in, got, want)
		}
	}
}

func TestSameShopLooksTheSameHoweverItWasPaid(t *testing.T) {
	c := &classifier{}
	a := c.classifyFull("WOOLWORTHS 1234 SYDNEY AUS", "", "", 40)
	b := c.classifyFull("WOOLWORTHS 1234 SYDNEY AUS - Visa Purchase - Receipt 147114 Copyright © 2026 X Banking Corporation ABN 1", "", "", 40)
	if a.Cat != "Groceries" || b.Cat != a.Cat || b.Conf != a.Conf {
		t.Errorf("%+v vs %+v", a, b)
	}
}

func TestProcessorPrefixesAreNotThePayee(t *testing.T) {
	for in, want := range map[string][]string{
		"ZLR*Deli Hub - St. Sub Suburbton AUS": {"deli", "hub"},
		"SQ *CLAY GREEN BAKE Suburbton":  {"clay", "green", "bake"},
		"SMP*Henris Patisserie Suburbton":  {"henris", "patisserie"},
		"LSP*Mrs Smith the Bake Suburbton AUS": {"mrs", "smith", "bake"},
		"Ezypay*Acme Fit Traini Sydney AUS":  {"acme", "fit", "traini"},
	} {
		got := descriptorTokens(in)
		if len(got) > len(want) {
			got = got[:len(want)]
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}

func TestStoredFooterTextIsRemovedOnce(t *testing.T) {
	a := testApp(t)
	withAccount(a)
	addTxn(t, a, "x1", "2026-03-01", "ALDI 88 - Visa Purchase - Receipt 1 Copyright © 2026 Westpac Banking Corporation ABN 33", 10, "Groceries", 90, false, "builtin")
	addTxn(t, a, "x2", "2026-03-02", "COLES 5", 10, "Groceries", 90, false, "builtin")
	n, err := cleanStoredNames(a.db)
	if err != nil || n != 1 {
		t.Fatalf("cleaned %d %v", n, err)
	}
	var name string
	a.db.QueryRow(`SELECT name FROM transactions WHERE plaid_txn_id='x1'`).Scan(&name)
	if name != "ALDI 88 - Visa Purchase - Receipt 1" {
		t.Errorf("name: %q", name)
	}
	if n, _ := cleanStoredNames(a.db); n != 0 {
		t.Errorf("second run should change nothing: %d", n)
	}
}

func TestReimportRecognisesRowsStoredUnderAnOlderDescription(t *testing.T) {
	a := testApp(t)
	c := newClient(t, a)
	// the earlier version stored this row with footer text and an id derived from that text
	a.db.Exec(`INSERT INTO accounts(plaid_account_id,item_id,name,type) VALUES('acct',NULL,'Everyday','manual')`)
	addTxn(t, a, "old-id-1", "2026-03-01", "ALDI STORES 88 AUS - Visa Purchase - Receipt 100160 Copyright © 2026 Example Banking Corporation", 54.2, "Groceries", 90, false, "builtin")
	csv := "Date,Description,Debit,Credit\n01/03/2026,ALDI STORES 88 AUS - Visa Purchase - Receipt 100160,54.20,\n02/03/2026,NETFLIX.COM,22.99,\n"
	_, tok := uploadMapping(t, c, csv)
	r := c.post("/import/run", url.Values{"token": {tok}, "date": {"0"}, "layout": {"1"}, "desc": {"1"}, "desc2": {"-1"}, "category": {"-1"}, "mode": {"split"},
		"amount": {"-1"}, "debit": {"2"}, "credit": {"3"}, "account": {"acct"}, "action": {"import"}})
	loc, _ := url.QueryUnescape(r.Header.Get("Location"))
	if !strings.Contains(loc, "Imported 1 transactions (1 duplicates skipped)") {
		t.Errorf("the ALDI row is already there under its old description: %s", loc)
	}
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE name LIKE 'ALDI%'`).Scan(&n)
	if n != 1 {
		t.Errorf("no duplicate ALDI row: %d", n)
	}
}

func TestGenuineRepeatPurchasesAreStillImported(t *testing.T) {
	a := testApp(t)
	c := newClient(t, a)
	a.db.Exec(`INSERT INTO accounts(plaid_account_id,item_id,name,type) VALUES('acct',NULL,'Everyday','manual')`)
	addTxn(t, a, "old-id-1", "2026-03-01", "COFFEE SHOP - Visa Purchase - Receipt 1 Copyright © 2026 X Banking Corporation", 5, "Dining & Takeaway", 90, false, "builtin")
	// two coffees on the same day for the same price: one is already stored, one is new
	csv := "Date,Description,Debit,Credit\n01/03/2026,COFFEE SHOP - Visa Purchase - Receipt 1,5.00,\n01/03/2026,COFFEE SHOP - Visa Purchase - Receipt 2,5.00,\n"
	_, tok := uploadMapping(t, c, csv)
	r := c.post("/import/run", url.Values{"token": {tok}, "date": {"0"}, "layout": {"1"}, "desc": {"1"}, "desc2": {"-1"}, "category": {"-1"}, "mode": {"split"},
		"amount": {"-1"}, "debit": {"2"}, "credit": {"3"}, "account": {"acct"}, "action": {"import"}})
	loc, _ := url.QueryUnescape(r.Header.Get("Location"))
	if !strings.Contains(loc, "Imported 1 transactions (1 duplicates skipped)") {
		t.Errorf("one of the two is new: %s", loc)
	}
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE name LIKE 'COFFEE%'`).Scan(&n)
	if n != 2 {
		t.Errorf("both coffees should be stored: %d", n)
	}
}
