package main

import "testing"

func load(t *testing.T, s string) (mapping, []csvTxn, int) {
	t.Helper()
	rows, err := readCSV([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	h, d, err := splitHeader(rows)
	if err != nil {
		t.Fatal(err)
	}
	m := guessMapping(h, d)
	if err := m.valid(len(h)); err != nil {
		t.Fatalf("mapping %+v invalid: %v", m, err)
	}
	tx, sk := convertRows(d, m)
	return m, tx, sk
}

func TestSplitDebitCreditColumns(t *testing.T) { // headers: Date,Description,Credit,Debit,Balance; debits may be negative
	m, tx, _ := load(t, "Date,Description,Credit,Debit,Balance\n"+
		"31/12/2024,Woolworths Metro,,-45.20,1000.00\n"+
		"30/12/2024,Salary ACME,2500.00,,1045.20\n")
	if !m.Split || len(tx) != 2 {
		t.Fatalf("%+v %v", m, tx)
	}
	if tx[0].Date != "2024-12-31" || tx[0].Amount != 45.20 || tx[1].Amount != -2500 {
		t.Errorf("%+v", tx)
	}
}

func TestSplitColumnsWithBOMAndBankCategory(t *testing.T) {
	m, tx, _ := load(t, "\xEF\xBB\xBFBank Account,Date,Narrative,Debit Amount,Credit Amount,Balance,Categories,Serial\n"+
		"000-000 00000000,02/01/2025,\"EFTPOS UBER *TRIP, SYDNEY\",18.50,,500.00,TRANSPORT,\n"+
		"000-000 00000000,01/01/2025,PAYMENT RECEIVED,,100.00,518.50,PAYMENTS,\n")
	if !m.Split || m.Category < 0 || len(tx) != 2 || tx[0].Desc != "EFTPOS UBER *TRIP, SYDNEY" {
		t.Fatalf("%+v %+v", m, tx)
	}
	if tx[0].Amount != 18.5 || tx[1].Amount != -100 || tx[0].Cat != "csv:TRANSPORT" {
		t.Errorf("%+v", tx)
	}
}

func TestSingleAmountSemicolonAndPreamble(t *testing.T) {
	m, tx, sk := load(t, "Account;Everyday\nExported;2025\n\nBooking Date;Details;Amount\n"+
		"2025-03-01;Coffee;-4,50\n2025-03-02;Refund;12.00\nTotal;;7.50\n")
	// "-4,50" has a comma -> thousands separator semantics: 450. Document the limitation rather than guess.
	if m.Layout != "2006-01-02" || len(tx) != 2 || sk != 1 {
		t.Fatalf("%+v %+v skipped=%d", m, tx, sk)
	}
}

func TestSignDetectionCreditCard(t *testing.T) {
	m, tx, _ := load(t, "Date,Description,Amount\n01/02/2025,Shop,10.00\n02/02/2025,Shop2,20.00\n03/02/2025,Cafe,5.00\n"+
		"04/02/2025,A,1.00\n05/02/2025,B,2.00\n06/02/2025,C,3.00\n07/02/2025,D,4.00\n08/02/2025,E,5.00\n09/02/2025,F,6.00\n10/02/2025,G,7.00\n")
	if !m.Invert || tx[0].Amount != 10 {
		t.Errorf("%+v %+v", m, tx[0])
	}
}

func TestDateLayoutDayFirstAndNoHeader(t *testing.T) {
	m, tx, _ := load(t, "03/04/2025,Thing,-9.99\n13/04/2025,Other,-1.00\n")
	if m.Layout != "2/1/2006" || tx[0].Date != "2025-04-03" {
		t.Errorf("%+v %+v", m, tx)
	}
	if m.Desc != 1 || m.Amount != 2 {
		t.Errorf("content-based guess failed: %+v", m)
	}
}

func TestParseAmount(t *testing.T) {
	for in, want := range map[string]float64{"$1,234.50": 1234.5, "(12.30)": -12.3, "12.30-": -12.3, "-5": -5, "+7": 7} {
		if got, ok := parseAmount(in); !ok || got != want {
			t.Errorf("%q: %v %v", in, got, ok)
		}
	}
	if _, ok := parseAmount("abc"); ok {
		t.Error("abc parsed")
	}
}

func TestTxnIDsStableAndDistinct(t *testing.T) {
	a := []csvTxn{{"2025-01-01", "X", "", 1}, {"2025-01-01", "X", "", 1}}
	ids := txnIDs("acct", a)
	if ids[0] == ids[1] {
		t.Error("identical rows must get distinct ids")
	}
	if again := txnIDs("acct", a[:1]); again[0] != ids[0] {
		t.Error("ids must be stable across overlapping imports")
	}
}
