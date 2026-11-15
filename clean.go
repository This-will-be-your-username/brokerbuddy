package main

import (
	"database/sql"
	"regexp"
	"strings"
)

// Bank descriptions carry wording about HOW the payment was made that says nothing about WHO was
// paid: "- Visa Purchase - Receipt 147114", "- Direct Debit - Receipt 139006", "Foreign Currency
// Amount ...", and (from PDF statements) page-footer text such as "Copyright © 2026 ... Banking
// Corporation". cleanText removes that before a description is matched against merchants, so the
// same shop looks the same however it was paid. Stored descriptions are not changed.
var noiseRes = []*regexp.Regexp{
	// "<some merchant> CRED VOUCHER <the real payee>": only the text after the marker says who was paid.
	regexp.MustCompile(`(?i)^.*\bcred(it)?\s+voucher\b`),
	regexp.MustCompile(`(?i)[-–]\s*(visa|mastercard|eftpos|visa debit)?\s*(purchase|payment|refund)\b`), // "- Visa Purchase"
	regexp.MustCompile(`(?i)[-–]\s*direct (debit|credit)\b`),                                            // "- Direct Debit"
	regexp.MustCompile(`(?i)\breceipt\s*\d[\d\s]*`),                                                     // "Receipt 139006 105"
	regexp.MustCompile(`(?i)foreign\s*curr\w*.*$`),                                                      // "Foreign Currency Amount: USD 9.99"
	regexp.MustCompile(`(?i)(copyright|©).*$`),                                                          // page footer
	regexp.MustCompile(`(?i)\b(westpac|commonwealth|anz|nab|ing)\s+banking\b.*$`),                       // footer text
}

func cleanText(s string) string {
	for _, re := range noiseRes {
		s = re.ReplaceAllString(s, " ")
	}
	return s
}

// footerRe recognises page-furniture lines in a PDF (legal text, page numbers, web addresses), which
// must never be glued onto a transaction's description.
var footerRe = regexp.MustCompile(`(?i)(copyright|©|all rights reserved|\babn\s*\d|\bacn\s*\d|\bafsl\b|australian (credit|financial services) licen[cs]e|\bpage\s+\d+\s*(of|/)\s*\d+|www\.|https?://|\.com\.au|\.com\b|customer (care|service)|statement (continues|continued|summary)|banking corporation|bank limited|please (check|retain|keep)|important information)`)

// ---- repairing data imported before footers were excluded ------------------------------------------

var footerCut = regexp.MustCompile(`(?i)\s*(copyright|©).*$`)

// cleanStoredNames removes page-footer text that an earlier version glued onto descriptions
// (ids are unaffected). It is idempotent and cheap: nothing matches once the data is clean.
func cleanStoredNames(db *sql.DB) (int, error) {
	rows, err := db.Query(`SELECT plaid_txn_id, name FROM transactions WHERE name LIKE '%opyright%' OR name LIKE '%©%'`)
	if err != nil {
		return 0, err
	}
	type fix struct{ id, name string }
	var fixes []fix
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return 0, err
		}
		if clean := strings.TrimSpace(footerCut.ReplaceAllString(name, "")); clean != "" && clean != name {
			fixes = append(fixes, fix{id, clean})
		}
	}
	rows.Close()
	if len(fixes) == 0 {
		return 0, nil
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, f := range fixes {
		if _, err := tx.Exec(`UPDATE transactions SET name=? WHERE plaid_txn_id=?`, f.name, f.id); err != nil {
			return 0, err
		}
	}
	return len(fixes), tx.Commit()
}

// legacyKey identifies a transaction's description well enough to recognise it after the wording
// was cleaned (receipt numbers, channel text and footers removed).
func legacyKey(s string) string {
	n := norm(cleanText(s))
	if len(n) > 16 {
		n = n[:16]
	}
	return n
}

// dupCheck finds transactions that were stored under an older version's description. Their ids
// (a hash of the description) no longer match a fresh import, so a re-import would add them
// again. A row counts as already imported when the same account has one on the same day for the
// same amount with the same start of description; each existing row can be matched only once, so
// genuine repeat purchases are still imported.
type dupCheck struct {
	tx       *sql.Tx
	acct     string
	consumed map[string]bool
}

func newDupCheck(tx *sql.Tx, acct string) *dupCheck {
	return &dupCheck{tx: tx, acct: acct, consumed: map[string]bool{}}
}

// seen reports whether the row is already in the database under another id.
func (d *dupCheck) seen(id, date string, amount float64, desc string) bool {
	var n int
	d.tx.QueryRow(`SELECT COUNT(*) FROM transactions WHERE plaid_txn_id=?`, id).Scan(&n)
	if n > 0 {
		d.consumed[id] = true // an ordinary duplicate: the insert will skip it
		return false
	}
	rows, err := d.tx.Query(`SELECT plaid_txn_id, name FROM transactions WHERE account_id=? AND date=? AND ABS(amount-?)<0.005`, d.acct, date, amount)
	if err != nil {
		return false
	}
	want := legacyKey(desc)
	var match string
	for rows.Next() {
		var tid, name string
		rows.Scan(&tid, &name)
		if match == "" && !d.consumed[tid] && legacyKey(name) == want {
			match = tid
		}
	}
	rows.Close()
	if match == "" {
		return false
	}
	d.consumed[match] = true
	return true
}
