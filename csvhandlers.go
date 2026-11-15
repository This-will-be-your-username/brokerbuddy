package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Uploaded files wait in memory between "upload" and "import" so the user can adjust the mapping.
type importSession struct {
	filename string
	header   []string
	data     [][]string
	notes    []string // shown above the mapping, e.g. what the PDF reader did
	created  time.Time
}

var sessions = struct {
	sync.Mutex
	m map[string]*importSession
}{m: map[string]*importSession{}}

func putSession(s *importSession) string {
	b := make([]byte, 16)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	sessions.Lock()
	defer sessions.Unlock()
	for k, v := range sessions.m { // expire old ones
		if time.Since(v.created) > 30*time.Minute {
			delete(sessions.m, k)
		}
	}
	if len(sessions.m) >= 20 {
		sessions.m = map[string]*importSession{}
	}
	sessions.m[tok] = s
	return tok
}

func getSession(tok string) *importSession {
	sessions.Lock()
	defer sessions.Unlock()
	return sessions.m[tok]
}

type optView struct {
	Value    int
	Label    string
	Selected bool
}
type acctOpt struct {
	ID, Name string
	Card     bool // flagged as a credit card
}
type previewRow struct {
	Date, Desc, Cat  string // Cat = the category column from the file, if any
	Amount           float64
	Pred, Level, Why string // predicted category, confidence level and the reason
	Conf             int
}

func colOpts(header []string, cur int, none bool) []optView {
	var o []optView
	if none {
		o = append(o, optView{-1, "(none)", cur < 0})
	}
	for i, h := range header {
		if h == "" {
			h = "(blank)"
		}
		o = append(o, optView{i, fmt.Sprintf("%d. %s", i+1, h), cur == i})
	}
	return o
}

func (a *app) manualAccounts() []acctOpt {
	rows, err := a.db.Query(`SELECT plaid_account_id, name, is_card FROM accounts WHERE item_id IS NULL ORDER BY name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []acctOpt
	for rows.Next() {
		var o acctOpt
		var card int
		rows.Scan(&o.ID, &o.Name, &card)
		o.Card = card == 1
		out = append(out, o)
	}
	return out
}

func (a *app) importPage(w http.ResponseWriter, r *http.Request) {
	a.tpl.ExecuteTemplate(w, "import.html", map[string]any{"Stage": "upload", "Error": r.URL.Query().Get("err")})
}

// isPDF checks the file signature rather than trusting the file name.
func isPDF(b []byte) bool {
	head := b
	if len(head) > 1024 {
		head = head[:1024]
	}
	return bytes.Contains(head, []byte("%PDF-"))
}

func (a *app) importUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		a.renderUploadError(w, errors.New("choose a CSV or PDF file (max 10 MB)"))
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		a.renderUploadError(w, err)
		return
	}
	if isPDF(raw) {
		res, perr := parsePDFStatement(raw, r.FormValue("pdf_password"))
		if perr != nil {
			a.renderUploadError(w, perr)
			return
		}
		notes := append([]string{fmt.Sprintf("Read %d transactions from %d page(s) of a PDF statement.", len(res.Rows), res.Pages)}, res.Notes...)
		s := &importSession{filename: hdr.Filename, header: res.Header, data: res.Rows, notes: notes, created: time.Now()}
		// PDF layouts differ per bank, so no saved CSV profile or account is pre-selected.
		pm := guessMapping(res.Header, res.Rows)
		orientCardSign(&pm, res.Rows)
		a.renderMapping(w, putSession(s), s, pm, "", "", "", "")
		return
	}
	rows, err := readCSV(raw)
	if err == nil {
		var header []string
		var data [][]string
		if header, data, err = splitHeader(rows); err == nil {
			s := &importSession{filename: hdr.Filename, header: header, data: data, created: time.Now()}
			m, acct := guessMapping(header, data), ""
			fromProfile := false
			var js string
			if a.db.QueryRow(`SELECT mapping, account_id FROM csv_profiles WHERE signature=?`, signature(header)).Scan(&js, &acct) == nil {
				var saved mapping
				if json.Unmarshal([]byte(js), &saved) == nil && saved.valid(len(header)) == nil {
					m, fromProfile = saved, true // same format as a previous import
				}
			}
			if !fromProfile {
				orientCardSign(&m, data) // a card statement's own payment line shows which way is spending
			}
			a.renderMapping(w, putSession(s), s, m, acct, "", "", "")
			return
		}
	}
	a.renderUploadError(w, err)
}

func (a *app) renderUploadError(w http.ResponseWriter, err error) {
	w.WriteHeader(http.StatusBadRequest)
	a.tpl.ExecuteTemplate(w, "import.html", map[string]any{"Stage": "upload", "Error": err.Error()})
}

// renderMapping shows the mapping/preview screen. cardState is "on"/"off" once the user has made a
// choice on the credit-card tickbox, or "" to start from the account's flag or the statement's look.
func (a *app) renderMapping(w http.ResponseWriter, tok string, s *importSession, m mapping, acct, newName, errMsg, cardState string) {
	conv, skipped := convertRows(s.data, m)
	suggestCard := looksLikeCardStatement(conv)
	cardChecked := suggestCard
	var acctCard int
	if acct != "" && acct != "new" && a.db.QueryRow(`SELECT is_card FROM accounts WHERE plaid_account_id=?`, acct).Scan(&acctCard) == nil {
		cardChecked = acctCard == 1
	}
	switch cardState {
	case "on":
		cardChecked = true
	case "off":
		cardChecked = false
	}
	var prev []previewRow
	levels := map[string]int{}
	if m.valid(len(s.header)) == nil {
		cl, err := newClassifier(a.db)
		for i, t := range conv {
			if err != nil {
				break
			}
			r := cl.classifyFull(t.Desc, "", t.Cat, t.Amount)
			lvl := confLevel(r.Conf, false, r.Cat)
			levels[lvl]++
			if i < 8 {
				prev = append(prev, previewRow{t.Date, t.Desc, strings.TrimPrefix(t.Cat, "csv:"), t.Amount, r.Cat, lvl, srcWhy(r.Src, false), r.Conf})
			}
		}
	}
	var layouts []optView
	for i, l := range dateLayouts {
		layouts = append(layouts, optView{i, l.Label, l.Layout == m.Layout})
	}
	sample := s.data
	if len(sample) > 4 {
		sample = sample[:4]
	}
	a.tpl.ExecuteTemplate(w, "import.html", map[string]any{
		"Stage": "map", "Token": tok, "Filename": s.filename, "Header": s.header, "Sample": sample,
		"Rows": len(s.data), "Skipped": skipped, "Valid": len(conv), "Preview": prev, "M": m, "Error": errMsg,
		"DateOpts": colOpts(s.header, m.Date, false), "DescOpts": colOpts(s.header, m.Desc, false),
		"Desc2Opts": colOpts(s.header, m.Desc2, true), "AmountOpts": colOpts(s.header, m.Amount, false),
		"DebitOpts": colOpts(s.header, m.Debit, false), "CreditOpts": colOpts(s.header, m.Credit, false),
		"CatOpts": colOpts(s.header, m.Category, true), "Layouts": layouts,
		"SuggestCard": suggestCard, "CardChecked": cardChecked,
		"Levels": levels, "Accounts": a.manualAccounts(), "Acct": acct, "NewName": newName, "Notes": s.notes,
	})
}

func formInt(r *http.Request, k string) int {
	n, err := strconv.Atoi(r.FormValue(k))
	if err != nil {
		return -1
	}
	return n
}

func (a *app) importRun(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	tok := r.FormValue("token")
	s := getSession(tok)
	if s == nil {
		http.Redirect(w, r, "/import?err="+url.QueryEscape("That upload expired — please upload the file again."), http.StatusSeeOther)
		return
	}
	m := mapping{
		Date: formInt(r, "date"), Desc: formInt(r, "desc"), Desc2: formInt(r, "desc2"),
		Amount: formInt(r, "amount"), Debit: formInt(r, "debit"), Credit: formInt(r, "credit"),
		Category: formInt(r, "category"), Split: r.FormValue("mode") == "split", Invert: r.FormValue("invert") == "on",
	}
	if i := formInt(r, "layout"); i >= 0 && i < len(dateLayouts) {
		m.Layout = dateLayouts[i].Layout
	}
	acct, newName := r.FormValue("account"), strings.TrimSpace(r.FormValue("new_name"))
	card := r.FormValue("is_card") == "on"
	cardState := "off"
	if card {
		cardState = "on"
	}

	if r.FormValue("action") != "import" {
		a.renderMapping(w, tok, s, m, acct, newName, "", cardState)
		return
	}
	bad := func(msg string) { a.renderMapping(w, tok, s, m, acct, newName, msg, cardState) }
	if err := m.valid(len(s.header)); err != nil {
		bad(err.Error())
		return
	}
	if acct == "" || acct == "new" {
		if newName == "" {
			bad("name the account (e.g. “Everyday Account”)")
			return
		}
	}
	txns, _ := convertRows(s.data, m)
	if len(txns) == 0 {
		bad("no rows could be read with these settings")
		return
	}

	if acct == "" || acct == "new" {
		b := make([]byte, 8)
		rand.Read(b)
		acct = "csv-" + hex.EncodeToString(b)
		if _, err := a.db.Exec(`INSERT INTO accounts(plaid_account_id,item_id,name,type,is_card) VALUES(?,NULL,?,'manual',?)`, acct, newName, boolInt(card)); err != nil {
			fail(w, err, 500)
			return
		}
	} else {
		var n int
		a.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE plaid_account_id=? AND item_id IS NULL`, acct).Scan(&n)
		if n == 0 {
			bad("unknown account")
			return
		}
		a.db.Exec(`UPDATE accounts SET is_card=? WHERE plaid_account_id=?`, boolInt(card), acct) // the tickbox is the account's type
	}

	cl, err := newClassifier(a.db)
	if err != nil {
		fail(w, err, 500)
		return
	}
	cctx := loadCardCtx(a.db)
	ids := txnIDs(acct, txns)
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, err, 500)
		return
	}
	defer tx.Rollback()
	added := 0
	levels := map[string]int{} // confidence of the transactions actually added
	skippedCard := 0           // card repayments that are ignored
	for i, t := range txns {
		cr := cl.classifyFull(t.Desc, "", t.Cat, t.Amount)
		res, err := tx.Exec(`INSERT INTO transactions(plaid_txn_id,account_id,date,name,merchant,amount,plaid_category,category,cat_source,confidence)
			VALUES(?,?,?,?,'',?,?,?,?,?) ON CONFLICT(plaid_txn_id) DO NOTHING`,
			ids[i], acct, t.Date, t.Desc, t.Amount, t.Cat, cr.Cat, cr.Src, cr.Conf)
		if err != nil {
			fail(w, err, 500)
			return
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
			if cctx.auto(card, cr.Cat, t.Amount, t.Desc) != ignoreNone {
				skippedCard++ // an ignored repayment: not part of the review summary
			} else {
				levels[confLevel(cr.Conf, false, cr.Cat)]++
			}
		}
	}
	js, _ := json.Marshal(m)
	tx.Exec(`INSERT INTO csv_profiles(signature,mapping,account_id) VALUES(?,?,?)
		ON CONFLICT(signature) DO UPDATE SET mapping=excluded.mapping, account_id=excluded.account_id`,
		signature(s.header), string(js), acct)
	if err := tx.Commit(); err != nil {
		fail(w, err, 500)
		return
	}
	sessions.Lock()
	delete(sessions.m, tok)
	sessions.Unlock()
	recomputeIgnored(a.db) // sets the flag on the new rows (and on other accounts' payments to this card)
	msg := fmt.Sprintf("Imported %d transactions (%d duplicates skipped).", added, len(txns)-added)
	if skippedCard > 0 {
		msg += fmt.Sprintf(" %d credit card repayment(s) ignored.", skippedCard)
	}
	dest := "/?msg="
	if need := levels["low"] + levels["unknown"]; need > 0 {
		// take the user straight to what needs a look, lowest confidence first
		msg += fmt.Sprintf(" %d high, %d medium, %d low confidence: review the ones below.", levels["high"], levels["medium"], need)
		dest = "/transactions?confidence=review&msg="
	} else if added > 0 {
		msg += " All were categorised with good confidence."
	}
	http.Redirect(w, r, dest+url.QueryEscape(msg), http.StatusSeeOther)
}

func (a *app) deleteAccount(w http.ResponseWriter, r *http.Request) {
	a.db.Exec(`DELETE FROM accounts WHERE plaid_account_id=? AND item_id IS NULL`, r.FormValue("id"))
	http.Redirect(w, r, "/settings#banks", http.StatusSeeOther)
}
