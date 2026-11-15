package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Repayments from other people.
//
// A small credit from another person ("OSKO PAYMENT FROM JANE SMITH dinner", "TRANSFER FROM TOM B
// swimming") is usually someone paying you back for something you paid for. It is not income:
// it should reduce spending in the category it relates to (restaurants, kids' activities, ...).
//
// Such credits are flagged as repayments from others. The category of a flagged credit is the
// spending category it offsets, and because a credit in a spending category already nets against
// spending everywhere (dashboard, charts, lender view, mortgage), setting that category is all
// that is needed. A flagged credit with no category yet is held as a neutral transfer (so it never
// counts as income) and listed for you to assign.
//
// Detection is deliberately conservative: only credits up to a size limit (default $300) whose
// wording is person-to-person, and not transfers between your own accounts, salary, benefits or
// interest. Anything can be flagged or unflagged by hand.

type reimbCfg struct {
	enabled bool
	max     float64
}

const defaultReimbMax = 300.0

func loadReimbCfg(db *sql.DB) reimbCfg {
	cfg := reimbCfg{enabled: true, max: defaultReimbMax}
	var v string
	if db.QueryRow(`SELECT value FROM settings WHERE key='reimb_enabled'`).Scan(&v) == nil && v == "0" {
		cfg.enabled = false
	}
	if db.QueryRow(`SELECT value FROM settings WHERE key='reimb_max'`).Scan(&v) == nil {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			cfg.max = f
		}
	}
	return cfg
}

var (
	// wording of a person-to-person payment
	reP2P = regexp.MustCompile(`(?i)\b(transfer from|tfr from|trf from|osko|payid|fast transfer|pay anyone|payment from|bank transfer|npp)\b`)
	// wording of a transfer between your own accounts or a financial product: not a repayment
	reOwnAcct = regexp.MustCompile(`(?i)\b(own account|savings|offset|term deposit|internal|mortgage|loan|investment|share trading|broker|from (cba|nab|anz|westpac|ing|macquarie|bendigo|suncorp|bankwest|st ?george))\b`)
)

// What the memo says it was for -> the spending category it offsets, most specific first.
var offsetCues = []struct {
	re  *regexp.Regexp
	cat string
}{
	{regexp.MustCompile(`(?i)\b(dinner|lunch|breakfast|brunch|coffee|cafe|restaurant|pizza|drinks?|pub|bar|takeaway|uber eats|menulog|doordash|meal|meals|food|bbq|sushi|thai|burgers?)\b`), "Dining & Takeaway"},
	{regexp.MustCompile(`(?i)\b(kids?|school|swim\w*|soccer|football|dance|netball|cricket|karate|piano|lessons?|excursion|camp|uniform|childcare|daycare|tutor\w*|sports?|term fees?|fees)\b`), "Education & Childcare"},
	{regexp.MustCompile(`(?i)\b(petrol|fuel|uber|taxi|toll|tolls|parking|train|bus|fare|fares)\b`), "Transport & Fuel"},
	{regexp.MustCompile(`(?i)\b(movies?|cinema|tickets?|concert|gig|show|golf|bowling|games?)\b`), "Entertainment"},
	{regexp.MustCompile(`(?i)\b(groceries|grocery|woolies|coles|aldi|shopping)\b`), "Groceries"},
	{regexp.MustCompile(`(?i)\b(gift|gifts|present|birthday|bday|wedding|flowers)\b`), "Gifts & Donations"},
	{regexp.MustCompile(`(?i)\b(holiday|flights?|accom\w*|airbnb|trip|hotel|travel)\b`), "Travel & Holidays"},
	{regexp.MustCompile(`(?i)\b(electricity|power|gas|internet|wifi|phone|bills?|water)\b`), "Utilities & Internet"},
	{regexp.MustCompile(`(?i)\b(rent|bond)\b`), "Rent"},
	{regexp.MustCompile(`(?i)\b(doctor|chemist|medical|dental|physio|vet)\b`), "Health & Medical"},
}

func offsetFromMemo(text string) (string, bool) {
	for _, c := range offsetCues {
		if c.re.MatchString(text) {
			return c.cat, true
		}
	}
	return "", false
}

func isSpendCat(cat string) bool {
	switch groupOf[cat] {
	case gEss, gDisc, gDebt, gRisk:
		return true
	}
	return false
}

// isHeldCat: categories where a flagged repayment has not been matched to spending yet.
func isHeldCat(cat string) bool {
	return cat == "Transfers" || cat == "Other Income" || cat == "Uncategorized"
}

// reimbursement decides whether a credit is a repayment from others, and which spending category
// it offsets. base is what the ordinary classifier made of it. override: 1 = the user says it is
// one, -1 = the user says it is not (handled by the caller), 0 = automatic.
func (c *classifier) reimbursement(name, merchant string, amount float64, base result, override int) result {
	text := norm(name + " " + merchant)
	forced := override == 1
	if !forced {
		if -amount > c.reimb.max || !reP2P.MatchString(text) || reOwnAcct.MatchString(text) {
			return base
		}
		switch base.Cat { // salary, benefits, rent received and savings moves are not repayments
		case "Salary & Wages", "Government Benefits", "Rental Income", "Savings & Investments":
			return base
		}
	}
	if isSpendCat(base.Cat) {
		base.Reimb = true // already assigned to what it offsets (your earlier choice, a rule or the memo)
		return base
	}
	if cat, ok := offsetFromMemo(text); ok {
		return result{Cat: cat, Src: "reimb", Conf: 60, Reimb: true}
	}
	return result{Cat: "Transfers", Src: "reimb", Conf: 35, Reimb: true} // held until you pick the category
}

// ---- handlers ----------------------------------------------------------------------------------

// setReimb flags (mode=yes) or unflags (mode=no) a credit as a repayment from others, or returns it
// to automatic detection (mode=auto).
func (a *app) setReimb(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("id")
	var over int
	switch r.FormValue("mode") {
	case "yes":
		over = 1
	case "no":
		over = -1
	}
	var amount float64
	var manual int
	if err := a.db.QueryRow(`SELECT amount, manual FROM transactions WHERE plaid_txn_id=?`, id).Scan(&amount, &manual); err != nil || amount >= 0 {
		fail(w, errBad, 400) // only credits can be repayments
		return
	}
	// rows you categorised yourself keep their category; the flag is set directly for them
	a.db.Exec(`UPDATE transactions SET reimb_override=?, reimb=CASE WHEN ?=1 THEN 1 WHEN ?=-1 THEN 0 ELSE reimb END WHERE plaid_txn_id=?`, over, over, over, id)
	if _, err := recategorize(a.db); err != nil {
		fail(w, err, 500)
		return
	}
	if over == 0 && manual == 1 {
		a.db.Exec(`UPDATE transactions SET reimb=0 WHERE plaid_txn_id=?`, id)
	}
	dest := "/transactions"
	if ref, err := url.Parse(r.Header.Get("Referer")); err == nil && ref.Path != "" {
		q := ref.Query()
		q.Del("msg")
		dest = ref.Path + "?" + q.Encode()
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// saveReimbSettings stores the automatic-detection switch and the size limit.
func (a *app) saveReimbSettings(w http.ResponseWriter, r *http.Request) {
	enabled := "0"
	if r.FormValue("enabled") == "on" {
		enabled = "1"
	}
	maxStr := strings.TrimSpace(strings.TrimPrefix(r.FormValue("max"), "$"))
	max, err := strconv.ParseFloat(maxStr, 64)
	if err != nil || max <= 0 || max > 100000 {
		settingsRedirect(w, r, "categorisation", "Enter the largest repayment to flag as a number between 1 and 100,000.", true)
		return
	}
	a.setSetting("reimb_enabled", enabled)
	a.setSetting("reimb_max", strconv.FormatFloat(max, 'f', -1, 64))
	if _, err := recategorize(a.db); err != nil {
		fail(w, err, 500)
		return
	}
	var n int
	a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE reimb=1 AND ignored=0`).Scan(&n)
	msg := fmt.Sprintf("Saved. %d repayment(s) from others are flagged.", n)
	if enabled == "0" {
		msg = "Saved. Automatic flagging is off; anything you flagged yourself stays flagged."
	}
	settingsRedirect(w, r, "categorisation", msg, false)
}

// unassignedReimb counts flagged repayments that have not been matched to a spending category.
func (a *app) unassignedReimb() (n int) {
	a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE reimb=1 AND ignored=0 AND category IN ('Transfers','Other Income','Uncategorized')`).Scan(&n)
	return
}

func (a *app) countReimb() (n int) {
	a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE reimb=1 AND ignored=0`).Scan(&n)
	return
}
