package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
)

// Credit cards.
//
// A statement for a credit card lists the purchases AND the payments made to the card. Those payments
// are the same money that leaves your bank account as "credit card repayment", so counting both
// the card's purchases and the repayments would count every dollar twice, and the payments into
// the card would look like income. Once an account is flagged as a credit card:
//
//   - repayments on the card itself (credits such as "PAYMENT RECEIVED - THANK YOU") are ignored;
//   - payments to a flagged card from your other accounts (category "Credit Card Repayments") are
//     ignored too, because the card's own purchases are now counted instead. This second rule can
//     be turned off in Settings.
//
// Ignored transactions stay in the database (so re-importing doesn't bring them back and you can
// see them), but are left out of the dashboard, charts, lender view and mortgage analysis.
// Any one of them can be counted anyway.

var (
	reCardPay    = regexp.MustCompile(`(?i)\b(payment|payments|thank you|repayment|autopay|auto pay|bpay|osko|direct debit|received|transfer|pay anyone|internet banking)\b`)
	reCardRefund = regexp.MustCompile(`(?i)\b(refund|reversal|reward|rewards|cashback|cash back|chargeback|dispute|adjustment|interest)\b`)
	reCardStrong = regexp.MustCompile(`(?i)\b(payment received|thank you|payment - thank|autopay|repayment)\b`)
)

// isCardRepayment: money coming INTO a card that is a payment from you rather than a refund.
func isCardRepayment(desc string, amount float64) bool {
	return amount < 0 && reCardPay.MatchString(desc) && !reCardRefund.MatchString(desc)
}

// looksLikeCardStatement: does this list of transactions look like a credit card statement?
// Wording such as "PAYMENT RECEIVED - THANK YOU" is specific to cards. The check ignores the sign,
// because the sign convention of the file may not have been worked out yet.
func looksLikeCardStatement(txns []csvTxn) bool {
	for _, t := range txns {
		if reCardStrong.MatchString(t.Desc) {
			return true
		}
	}
	return false
}

// orientCardSign sets "positive numbers are money spent" from the statement's own payment line:
// the payment into the card is the one amount that moves the other way from the purchases. Only
// meaningful for a single signed amount column.
func orientCardSign(m *mapping, data [][]string) {
	if m.Split || m.Amount < 0 || m.Desc < 0 {
		return
	}
	for _, r := range data {
		if m.Desc >= len(r) || m.Amount >= len(r) || !reCardStrong.MatchString(r[m.Desc]) {
			continue
		}
		if v, ok := parseAmount(r[m.Amount]); ok && v != 0 {
			m.Invert = v < 0 // payment shown negative => purchases are positive
			return
		}
	}
}

const (
	ignoreNone          = ""
	ignoreCardPayment   = "card_repayment"   // a payment into the card, seen on the card's own statement
	ignorePaidElsewhere = "card_payment_out" // the matching payment leaving another account
)

type cardCtx struct{ anyCard, elsewhere bool }

func loadCardCtx(db *sql.DB) cardCtx {
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE is_card=1`).Scan(&n)
	var v string
	db.QueryRow(`SELECT value FROM settings WHERE key='ignore_card_payments_elsewhere'`).Scan(&v)
	return cardCtx{anyCard: n > 0, elsewhere: v != "0"} // on unless switched off
}

// auto says why a transaction would be ignored (empty = it counts).
func (c cardCtx) auto(acctIsCard bool, cat string, amount float64, desc string) string {
	if !c.anyCard {
		return ignoreNone
	}
	if acctIsCard {
		if isCardRepayment(desc, amount) {
			return ignoreCardPayment
		}
		return ignoreNone
	}
	if c.elsewhere && cat == "Credit Card Repayments" && amount > 0 {
		return ignorePaidElsewhere
	}
	return ignoreNone
}

// ignoreWhy is the text shown for an ignored transaction.
func ignoreWhy(reason string) string {
	switch reason {
	case ignoreCardPayment:
		return "A payment into your credit card. It is the same money as the payment from your other account, so it isn't counted again."
	case ignorePaidElsewhere:
		return "A payment to your credit card. The card's own purchases are counted instead, so this isn't counted twice."
	}
	return ""
}

// recomputeIgnored sets every transaction's effective ignored flag from the account flags, the
// rule above and any per-transaction override (1 = always ignore, -1 = always count).
// It returns how many transactions are ignored.
func recomputeIgnored(db *sql.DB) (int, error) {
	ctx := loadCardCtx(db)
	rows, err := db.Query(`SELECT t.plaid_txn_id, t.name, t.amount, t.category, t.ignore_override, t.ignored, a.is_card
		FROM transactions t JOIN accounts a ON a.plaid_account_id=t.account_id`)
	if err != nil {
		return 0, err
	}
	type change struct {
		id  string
		val int
	}
	var changes []change
	total := 0
	for rows.Next() {
		var id, name, cat string
		var amt float64
		var override, cur, card int
		if err := rows.Scan(&id, &name, &amt, &cat, &override, &cur, &card); err != nil {
			rows.Close()
			return 0, err
		}
		want := 0
		switch {
		case override == 1:
			want = 1
		case override == -1:
			want = 0
		case ctx.auto(card == 1, cat, amt, name) != ignoreNone:
			want = 1
		}
		total += want
		if want != cur {
			changes = append(changes, change{id, want})
		}
	}
	rows.Close()
	if len(changes) == 0 {
		return total, nil
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, c := range changes {
		if _, err := tx.Exec(`UPDATE transactions SET ignored=? WHERE plaid_txn_id=?`, c.val, c.id); err != nil {
			return 0, err
		}
	}
	return total, tx.Commit()
}

func (a *app) countIgnored(accountID string) (n int) {
	if accountID == "" {
		a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE ignored=1`).Scan(&n)
	} else {
		a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE ignored=1 AND account_id=?`, accountID).Scan(&n)
	}
	return
}

// ---- handlers ----------------------------------------------------------------------------------

// flagAccount marks or unmarks an account as a credit card.
func (a *app) flagAccount(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	card := 0
	if r.FormValue("card") == "on" {
		card = 1
	}
	res, err := a.db.Exec(`UPDATE accounts SET is_card=? WHERE plaid_account_id=?`, card, id)
	if err != nil {
		fail(w, err, 500)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		fail(w, errBad, 400)
		return
	}
	a.afterCardChange(w, r, id, card == 1)
}

func (a *app) afterCardChange(w http.ResponseWriter, r *http.Request, id string, isCard bool) {
	if _, err := recomputeIgnored(a.db); err != nil {
		fail(w, err, 500)
		return
	}
	var name string
	a.db.QueryRow(`SELECT name FROM accounts WHERE plaid_account_id=?`, id).Scan(&name)
	msg := fmt.Sprintf("“%s” is no longer treated as a credit card.", name)
	if isCard {
		msg = fmt.Sprintf("“%s” is now a credit card account. %d transaction(s) are ignored in total (repayments into the card and payments to it).", name, a.countIgnored(""))
	}
	http.Redirect(w, r, "/settings?msg="+url.QueryEscape(msg)+"#banks", http.StatusSeeOther)
}

// saveCardSettings stores the "also ignore payments from other accounts" switch.
func (a *app) saveCardSettings(w http.ResponseWriter, r *http.Request) {
	v := "0"
	if r.FormValue("elsewhere") == "on" {
		v = "1"
	}
	a.setSetting("ignore_card_payments_elsewhere", v)
	n, _ := recomputeIgnored(a.db)
	http.Redirect(w, r, "/settings?msg="+url.QueryEscape(fmt.Sprintf("Saved. %d transaction(s) are ignored.", n))+"#banks", http.StatusSeeOther)
}

// setIgnoreOverride lets the user count an ignored transaction anyway (mode=include) or go back to
// the automatic rule (mode=auto).
func (a *app) setIgnoreOverride(w http.ResponseWriter, r *http.Request) {
	v := 0
	if r.FormValue("mode") == "include" {
		v = -1
	}
	if _, err := a.db.Exec(`UPDATE transactions SET ignore_override=? WHERE plaid_txn_id=?`, v, r.FormValue("id")); err != nil {
		fail(w, err, 500)
		return
	}
	recomputeIgnored(a.db)
	dest := "/transactions"
	if ref, err := url.Parse(r.Header.Get("Referer")); err == nil && ref.Path != "" {
		q := ref.Query()
		q.Del("msg")
		dest = ref.Path + "?" + q.Encode()
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

type accountRow struct {
	ID, Name, Source string
	Card             bool
	Ignored          int // transactions in this account that are ignored
}

// accountRows lists every account (linked or imported) for the credit-card settings.
func (a *app) accountRows() []accountRow {
	rows, err := a.db.Query(`SELECT a.plaid_account_id, a.name||CASE WHEN a.mask<>'' THEN ' ••'||a.mask ELSE '' END,
		CASE WHEN a.item_id IS NULL THEN 'Imported' ELSE COALESCE(i.institution,'Linked') END, a.is_card,
		(SELECT COUNT(*) FROM transactions t WHERE t.account_id=a.plaid_account_id AND t.ignored=1)
		FROM accounts a LEFT JOIN items i ON i.id=a.item_id ORDER BY a.name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []accountRow
	for rows.Next() {
		var r accountRow
		var card int
		rows.Scan(&r.ID, &r.Name, &r.Source, &card, &r.Ignored)
		r.Card = card == 1
		out = append(out, r)
	}
	return out
}
