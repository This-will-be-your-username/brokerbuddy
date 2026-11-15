package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type expTxn struct {
	Date   string  `json:"date"`
	Desc   string  `json:"desc"`
	Debit  float64 `json:"debit"`
	Credit float64 `json:"credit"`
}

func loadFixture(t *testing.T, name string) ([]byte, []expTxn) {
	t.Helper()
	pdfBytes, err := os.ReadFile(filepath.Join("testdata", "pdf", name+".pdf"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "pdf", name+".expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var exp []expTxn
	if err := json.Unmarshal(raw, &exp); err != nil {
		t.Fatal(err)
	}
	return pdfBytes, exp
}

// signedOut returns amount with money OUT positive, as the importer stores it.
func (e expTxn) signedOut() float64 { return round2(e.Debit - e.Credit) }

// rowsToOut converts parser output (any header shape) to "date|signed money-out" keys.
func rowsToOut(t *testing.T, res *pdfResult) []string {
	t.Helper()
	idx := map[string]int{}
	for i, h := range res.Header {
		idx[h] = i
	}
	var out []string
	for _, r := range res.Rows {
		var v float64
		if i, ok := idx["Debit"]; ok {
			d, _ := strconv.ParseFloat(r[i], 64)
			c, _ := strconv.ParseFloat(r[idx["Credit"]], 64)
			v = d - c
		} else {
			a, _ := strconv.ParseFloat(r[idx["Amount"]], 64)
			v = a // sign convention is decided later in the mapping screen
		}
		out = append(out, fmt.Sprintf("%s|%.2f", r[0], v))
	}
	return out
}

func keys(exp []expTxn, flip bool) []string {
	var out []string
	for _, e := range exp {
		v := e.signedOut()
		if flip {
			v = -v
		}
		out = append(out, fmt.Sprintf("%s|%.2f", e.Date, v))
	}
	return out
}

func sameMultiset(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func TestPDFFixtures(t *testing.T) {
	cases := []struct {
		name       string
		wantHeader []string
		flip       bool // single signed Amount column: purchases positive means "out" is positive already
	}{
		{"bank_a", []string{"Date", "Description", "Debit", "Credit", "Balance"}, false},
		{"bank_b", []string{"Date", "Description", "Debit", "Credit", "Balance"}, false},
		{"card_c", []string{"Date", "Description", "Amount"}, false},
		{"nohdr_d", []string{"Date", "Description", "Debit", "Credit", "Balance"}, false},
		{"reverse_e", []string{"Date", "Description", "Debit", "Credit", "Balance"}, false},
		{"daily_f", []string{"Date", "Description", "Debit", "Credit", "Balance"}, false},
		{"twodate_g", []string{"Date", "Description", "Amount"}, false},
		{"mono_h", []string{"Date", "Description", "Debit", "Credit", "Balance"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data, exp := loadFixture(t, c.name)
			res, err := parsePDFStatement(data, "")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(res.Header, ",") != strings.Join(c.wantHeader, ",") {
				t.Errorf("header %v want %v", res.Header, c.wantHeader)
			}
			got := rowsToOut(t, res)
			if !sameMultiset(got, keys(exp, c.flip)) {
				t.Errorf("transactions differ.\n got %d rows, want %d\n got: %v\nwant: %v", len(got), len(exp), got, keys(exp, c.flip))
			}
		})
	}
}

func TestPDFMultiLineDescriptionsAndFurniture(t *testing.T) {
	data, exp := loadFixture(t, "bank_a")
	res, err := parsePDFStatement(data, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != len(exp) {
		t.Fatalf("rows %d want %d (opening/closing balance lines, footers and repeated headers must be dropped)", len(res.Rows), len(exp))
	}
	// the second line of each description ("Card purchase") is joined onto the first
	joined := 0
	for _, r := range res.Rows {
		if strings.Contains(r[1], "Card purchase") || strings.Contains(r[1], "Direct debit") || strings.Contains(r[1], "Eftpos purchase") || strings.Contains(r[1], "Direct credit") || strings.Contains(r[1], "Visa purchase") || strings.Contains(r[1], "Cash") {
			joined++
		}
		if strings.Contains(strings.ToUpper(r[1]), "BALANCE") || strings.Contains(strings.ToLower(r[1]), "total debits") {
			t.Errorf("non-transaction row leaked: %v", r)
		}
	}
	if joined < len(exp)*8/10 {
		t.Errorf("only %d descriptions had their continuation line joined", joined)
	}
}

func TestPDFYearFromStatementPeriod(t *testing.T) {
	data, _ := loadFixture(t, "bank_b") // dates printed as "05 Nov" only
	res, err := parsePDFStatement(data, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Rows {
		if !strings.HasPrefix(r[0], "2025-11-") {
			t.Fatalf("year not completed from the period line: %v", r)
		}
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "statement period") {
		t.Errorf("expected a note about the period: %v", res.Notes)
	}
}

func TestPDFBalanceResolvesDirectionNewestFirst(t *testing.T) {
	data, exp := loadFixture(t, "reverse_e")
	res, err := parsePDFStatement(data, "")
	if err != nil {
		t.Fatal(err)
	}
	var credits, debits int
	for _, r := range res.Rows {
		if r[3] != "" {
			credits++
		}
		if r[2] != "" {
			debits++
		}
	}
	wantC := 0
	for _, e := range exp {
		if e.Credit > 0 {
			wantC++
		}
	}
	if credits != wantC || credits+debits != len(exp) {
		t.Errorf("credits %d debits %d, want %d credits of %d", credits, debits, wantC, len(exp))
	}
}

func TestPDFErrors(t *testing.T) {
	if _, err := parsePDFStatement([]byte("%PDF-1.4\nnot really"), ""); err == nil {
		t.Error("garbage must fail")
	}
	if _, err := parsePDFStatement([]byte("hello"), ""); err == nil {
		t.Error("non-PDF must fail")
	}
}

func TestPeelDate(t *testing.T) {
	for in, want := range map[string]string{
		"12/01/2025 COFFEE": "12/1/2025",
		"2025-10-03":        "2025-10-3",
		"05 Oct 25 Shop":    "5 Oct 2025",
		"5 October 2025":    "5 Oct 2025",
		"Oct 5, 2025":       "5 Oct 2025",
		"12 Jan":            "12 Jan",
		"31/12":             "?31/12",
	} {
		d, ok := peelDate(in)
		if !ok {
			t.Errorf("%q not parsed", in)
			continue
		}
		var got string
		switch {
		case d.numeric && !d.hasYear:
			got = fmt.Sprintf("?%d/%d", d.a, d.b)
		case d.numeric:
			got = fmt.Sprintf("%d/%d/%d", d.a, d.b, d.y)
		case d.hasYear:
			got = fmt.Sprintf("%d %s %d", d.d, []string{"", "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}[d.m], d.y)
			if strings.HasPrefix(in, "2025-") {
				got = fmt.Sprintf("%d-%d-%d", d.y, d.m, d.d)
			}
		default:
			got = fmt.Sprintf("%d %s", d.d, []string{"", "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}[d.m])
		}
		if got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	for _, bad := range []string{"Description", "Opening balance", "45.20", "99/99/2025", "Total"} {
		if _, ok := peelDate(bad); ok {
			t.Errorf("%q should not be a date", bad)
		}
	}
}

func TestReadAmount(t *testing.T) {
	cases := []struct {
		in  string
		mag float64
		dir int
	}{{"1,234.56", 1234.56, 0}, {"45.20 DR", 45.2, -1}, {"2,500.00 CR", 2500, 1}, {"-400.00", 400, -2}, {"(12.30)", 12.3, -2}, {"12.30-", 12.3, -2}}
	for _, c := range cases {
		m, d, ok := readAmount(c.in)
		if !ok || math.Abs(m-c.mag) > 0.001 || d != c.dir {
			t.Errorf("%q: %v %v %v", c.in, m, d, ok)
		}
	}
	for _, s := range []string{"1,234.56", "45.20 DR", "$12.00", "-400.00", "(12.30)"} {
		if !isAmountSpan(s) {
			t.Errorf("%q should be an amount", s)
		}
	}
	for _, s := range []string{"12345", "REF 12.30", "12/01/2025", "abc"} {
		if isAmountSpan(s) {
			t.Errorf("%q should not be an amount", s)
		}
	}
}

func TestPDFScannedImageIsRejectedClearly(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "pdf", "scan_i.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = parsePDFStatement(data, "")
	if err != errPDFNoText {
		t.Errorf("want the no-text error, got %v", err)
	}
}

// Page footers (legal small print) must not be glued onto the last transaction of a page.
func TestPDFFooterIsNotPartOfAnyTransaction(t *testing.T) {
	data, exp := loadFixture(t, "footer_j")
	res, err := parsePDFStatement(data, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Pages < 2 {
		t.Fatalf("the fixture should span pages: %d", res.Pages)
	}
	if len(res.Rows) != len(exp) {
		t.Fatalf("rows %d want %d", len(res.Rows), len(exp))
	}
	for _, r := range res.Rows {
		low := strings.ToLower(r[1])
		for _, bad := range []string{"copyright", "banking corporation", "abn", "afsl", "www.", "terms and conditions", "©"} {
			if strings.Contains(low, bad) {
				t.Errorf("footer text leaked into a description: %q", r[1])
			}
		}
	}
	if !sameMultiset(rowsToOut(t, res), keys(exp, false)) {
		t.Error("transactions differ from the fixture")
	}
}
