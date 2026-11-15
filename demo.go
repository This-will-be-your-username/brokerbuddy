package main

import (
	"database/sql"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"time"
)

// Demo data: a year of invented transactions for trying the app, testing, and taking screenshots
// without any real bank data. Everything is fictional (made-up people, shops and amounts) apart
// from well-known national brands used as examples. Demo rows live in accounts whose ids start
// with "demo_", so they can be removed with one click and never mix with imported data.

const demoPrefix = "demo_"

type demoTxn struct {
	acct, date, desc string
	amount           float64 // positive = money out
}

// demoTransactions builds a year of transactions ending on `end`. The same seed gives the same data.
func demoTransactions(end time.Time) []demoTxn {
	rng := rand.New(rand.NewSource(42))
	var out []demoTxn
	add := func(acct string, d time.Time, desc string, amt float64) {
		out = append(out, demoTxn{demoPrefix + acct, d.Format("2006-01-02"), desc, round2(amt)})
	}
	between := func(lo, hi float64) float64 { return lo + rng.Float64()*(hi-lo) }
	pick := func(s ...string) string { return s[rng.Intn(len(s))] }
	receipt := func() string { return fmt.Sprint(100000 + rng.Intn(899999)) }

	start := end.AddDate(0, 0, -364)
	groceries := []string{"WOOLWORTHS 1234 EXAMPLEVILLE AUS", "COLES 5678 EXAMPLEVILLE AUS", "ALDI STORES 90 SAMPLETON AUS", "IGA EXAMPLEVILLE AUS"}
	cafes := []string{"ACME COFFEE ROASTERS EXAMPLEVILLE AUS", "SUNNY BAKEHOUSE SAMPLETON AUS", "THE CORNER DELI EXAMPLEVILLE AUS", "STARBUCKS 22 SAMPLETON AUS"}
	takeaway := []string{"MCDONALDS 3321 SAMPLETON AUS", "DOMINOS PIZZA EXAMPLEVILLE", "GUZMAN Y GOMEZ EXAMPLEVILLE AUS", "UBER *EATS HELP.UBER.COM", "PEPPERCORN THAI EXAMPLEVILLE AUS", "TANAKA SUSHI SAMPLETON AUS"}
	fuel := []string{"BP 5566 SAMPLETON AUS", "AMPOL FOODARY EXAMPLEVILLE AUS", "7-ELEVEN 2201 SAMPLETON AUS"}
	retail := []string{"KMART 1042 SAMPLETON AUS", "BUNNINGS 7788 SAMPLETON AUS", "AMAZON AU MARKETPLACE SYDNEY", "JB HI-FI SAMPLETON AUS", "CHEMIST WAREHOUSE SAMPLETON", "COTTON ON EXAMPLEVILLE AUS"}
	// small local businesses the built-in rules cannot know: left for the user to categorise
	unknown := []string{"ZQ HOLDINGS 4471 EXAMPLEVILLE AUS", "J AND M ENTERPRISES SAMPLETON AUS", "KELLYS NOOK EXAMPLEVILLE AUS", "ORCHARD LANE EXAMPLEVILLE AUS"}
	friends := []string{"ALEX SAMPLE", "JORDAN EXAMPLE", "TAYLOR DEMO"}

	holiday := end.AddDate(0, -4, 0) // one trip, four months ago
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		day, wd := d.Day(), d.Weekday()
		// income and fixed bills
		if int(d.Sub(start).Hours()/24)%14 == 3 {
			add("everyday", d, "ACME SOFTWARE PTY LTD PAYROLL", -between(3380, 3420))
		}
		if day == 25 {
			add("everyday", d, "SAMPLE HEALTH SERVICES SALARY", -between(5150, 5250))
		}
		if day == 1 {
			add("everyday", d, "Rent - 14 Example St - Direct Debit - Receipt "+receipt(), 2650)
			add("everyday", d, "Transfer to Demo Savings", 800)
			add("savings", d, "Transfer from Demo Everyday", -800)
		}
		if day == 5 {
			add("everyday", d, "AGL ENERGY - Direct Debit - Receipt "+receipt(), between(110, 190))
			add("everyday", d, "TELSTRA - Direct Debit", 89)
			add("everyday", d, "AUSSIE BROADBAND", 79)
		}
		if day == 8 {
			add("everyday", d, "NRMA INSURANCE - Direct Debit", 142.5)
			add("everyday", d, "MEDIBANK PRIVATE - Direct Debit", 268)
		}
		if day == 12 {
			add("everyday", d, "NETFLIX.COM", 22.99)
			add("everyday", d, "SPOTIFY P1D4B2", 13.99)
			add("everyday", d, "APPLE.COM/BILL SYDNEY AUS", 4.49)
		}
		if day == 15 {
			add("everyday", d, "Acme Fit Training - Direct Debit - Receipt 107722 EZYPAY", 55)
		}
		if day == 20 {
			pay := round2(between(1400, 2100))
			add("everyday", d, "CREDIT CARD PAYMENT - Demo Bank Visa - BPAY", pay)
			add("card", d, "PAYMENT RECEIVED - THANK YOU", -pay)
		}
		if day == 28 {
			add("savings", d, "INTEREST PAID", -between(18, 26))
			if d.Month()%3 == 0 {
				add("everyday", d, "SYDNEY WATER CORPORATION - BPAY", between(180, 240))
			}
		}
		// day-to-day spending
		if wd == time.Saturday || wd == time.Wednesday {
			add("everyday", d, pick(groceries...)+" - Visa Purchase - Receipt "+receipt(), between(70, 190))
		}
		if wd == time.Sunday && rng.Intn(2) == 0 {
			add("card", d, pick(groceries...), between(25, 90))
		}
		if rng.Intn(3) == 0 {
			add("card", d, pick(cafes...), between(4.5, 19))
		}
		if rng.Intn(5) == 0 {
			add("card", d, pick(takeaway...), between(14, 68))
		}
		if wd == time.Friday && rng.Intn(3) == 0 {
			add("card", d, pick("THE EXAMPLE HOTEL SAMPLETON AUS", "BLUE GUM RESTAURANT EXAMPLEVILLE AUS", "VINTAGE CELLARS SAMPLETON AUS"), between(45, 160))
			if rng.Intn(2) == 0 { // a friend pays their share back
				add("everyday", d.AddDate(0, 0, 1), pick(friends...)+" Dinner - Osko Payment - Receipt "+receipt(), -round2(between(28, 55)))
			}
		}
		if rng.Intn(6) == 0 {
			add("everyday", d, pick(fuel...), between(55, 98))
		}
		if wd == time.Monday && rng.Intn(2) == 0 {
			add("everyday", d, "OPAL TOP UP SYDNEY", 40)
		}
		if rng.Intn(9) == 0 {
			add("card", d, pick(retail...), between(18, 240))
		}
		if rng.Intn(25) == 0 {
			add("everyday", d, "ATM WITHDRAWAL SAMPLETON", float64(100*(1+rng.Intn(2))))
		}
		if rng.Intn(11) == 0 {
			add("card", d, pick(unknown...), between(22, 210))
		}
		if wd == time.Tuesday && d.Month()%2 == 0 && day <= 7 {
			add("everyday", d, "Sampleton Swimming - Direct Debit - Receipt 17682", 34)
		}
		if day == 10 && d.Month()%3 == 1 {
			add("everyday", d, "BLUE GUM PHYSIOTHERAPY SAMPLETON AUS", between(95, 140))
		}
		if d.Equal(holiday) {
			add("card", d, "QANTAS AIRWAYS 08123456789 SYDNEY", 1480)
			add("card", d.AddDate(0, 0, 3), "CROWNE PLAZA SEASIDE SEASIDE AUS", 1260)
			add("card", d.AddDate(0, 0, 4), "HIPCAMP BOOKING", 320)
			add("card", d.AddDate(0, 0, 5), "AVIS RENT A CAR SEASIDE AUS", 410)
		}
	}
	return out
}

func demoExists(db *sql.DB) bool {
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE plaid_account_id LIKE ?`, demoPrefix+"%").Scan(&n)
	return n > 0
}

func hasRealTransactions(db *sql.DB) bool {
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE account_id NOT LIKE ?`, demoPrefix+"%").Scan(&n)
	return n > 0
}

// loadDemo adds the demo accounts and transactions. It refuses to run if you already have your own
// transactions, so demo data is never mixed into a real analysis. It returns how many were added.
func loadDemo(db *sql.DB, end time.Time) (int, error) {
	if hasRealTransactions(db) {
		return 0, fmt.Errorf("this database already holds your own transactions; use a separate database for the demo (start with -db demo.db -demo)")
	}
	if demoExists(db) {
		return 0, fmt.Errorf("demo data is already loaded")
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, ac := range []struct {
		id, name, mask string
		card           int
	}{
		{"everyday", "Demo Everyday", "1234", 0},
		{"savings", "Demo Savings", "5678", 0},
		{"card", "Demo Visa", "9012", 1},
	} {
		if _, err := tx.Exec(`INSERT INTO accounts(plaid_account_id,item_id,name,mask,type,is_card) VALUES(?,NULL,?,?,'manual',?)`, demoPrefix+ac.id, ac.name, ac.mask, ac.card); err != nil {
			return 0, err
		}
	}
	txns := demoTransactions(end)
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	cl, err := newClassifier(db)
	if err != nil {
		return 0, err
	}
	tx, err = db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for i, t := range txns {
		r := cl.classifyFull(t.desc, "", "", t.amount)
		if _, err := tx.Exec(`INSERT INTO transactions(plaid_txn_id,account_id,date,name,merchant,amount,plaid_category,category,cat_source,confidence,reimb)
			VALUES(?,?,?,?,'',?,'',?,?,?,?)`, fmt.Sprintf("%s%05d", demoPrefix, i), t.acct, t.date, t.desc, t.amount, r.Cat, r.Src, r.Conf, boolInt(r.Reimb)); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if _, err := recategorize(db); err != nil { // also works out which card repayments are ignored
		return 0, err
	}
	return len(txns), nil
}

// removeDemo deletes the demo accounts and their transactions (and nothing else).
func removeDemo(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM transactions WHERE account_id LIKE ?`, demoPrefix+"%"); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM accounts WHERE plaid_account_id LIKE ?`, demoPrefix+"%"); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *app) demoLoad(w http.ResponseWriter, r *http.Request) {
	n, err := loadDemo(a.db, time.Now())
	msg := fmt.Sprintf("Loaded %d demo transactions. Remove them any time under Settings → Demo data.", n)
	if err != nil {
		msg = err.Error()
	}
	http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (a *app) demoRemove(w http.ResponseWriter, r *http.Request) {
	msg := "Demo data removed."
	if err := removeDemo(a.db); err != nil {
		msg = err.Error()
	}
	http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}
