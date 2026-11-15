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
type acctOpt struct{ ID, Name string }
type previewRow struct {
	Date, Desc, Cat string
	Amount          float64
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
	rows, err := a.db.Query(`SELECT plaid_account_id, name FROM accounts WHERE item_id IS NULL ORDER BY name`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []acctOpt
	for rows.Next() {
		var o acctOpt
		rows.Scan(&o.ID, &o.Name)
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
		a.renderMapping(w, putSession(s), s, guessMapping(res.Header, res.Rows), "", "", "", "")
		return
	}
	rows, err := readCSV(raw)
	if err == nil {
		var header []string
		var data [][]string
		if header, data, err = splitHeader(rows); err == nil {
			s := &importSession{filename: hdr.Filename, header: header, data: data, created: time.Now()}
			m, acct := guessMapping(header, data), ""
			var js string
			if a.db.QueryRow(`SELECT mapping, account_id FROM csv_profiles WHERE signature=?`, signature(header)).Scan(&js, &acct) == nil {
				var saved mapping
				if json.Unmarshal([]byte(js), &saved) == nil && saved.valid(len(header)) == nil {
					m = saved // same format as a previous import
				}
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

func (a *app) renderMapping(w http.ResponseWriter, tok string, s *importSession, m mapping, acct, newName, errMsg, _ string) {
	conv, skipped := convertRows(s.data, m)
	var prev []previewRow
	if m.valid(len(s.header)) == nil {
		for i, t := range conv {
			if i >= 8 {
				break
			}
			prev = append(prev, previewRow{t.Date, t.Desc, strings.TrimPrefix(t.Cat, "csv:"), t.Amount})
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
		"Accounts": a.manualAccounts(), "Acct": acct, "NewName": newName, "Notes": s.notes,
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

	if r.FormValue("action") != "import" {
		a.renderMapping(w, tok, s, m, acct, newName, "", "")
		return
	}
	bad := func(msg string) { a.renderMapping(w, tok, s, m, acct, newName, msg, "") }
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
		if _, err := a.db.Exec(`INSERT INTO accounts(plaid_account_id,item_id,name,type) VALUES(?,NULL,?,'manual')`, acct, newName); err != nil {
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
	}

	cl, err := newClassifier(a.db)
	if err != nil {
		fail(w, err, 500)
		return
	}
	ids := txnIDs(acct, txns)
	tx, err := a.db.Begin()
	if err != nil {
		fail(w, err, 500)
		return
	}
	defer tx.Rollback()
	added := 0
	for i, t := range txns {
		cat, src := cl.classify(t.Desc, "", t.Cat, t.Amount)
		res, err := tx.Exec(`INSERT INTO transactions(plaid_txn_id,account_id,date,name,merchant,amount,plaid_category,category,cat_source)
			VALUES(?,?,?,?,'',?,?,?,?) ON CONFLICT(plaid_txn_id) DO NOTHING`,
			ids[i], acct, t.Date, t.Desc, t.Amount, t.Cat, cat, src)
		if err != nil {
			fail(w, err, 500)
			return
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
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
	msg := fmt.Sprintf("Imported %d transactions (%d duplicates skipped)", added, len(txns)-added)
	http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (a *app) deleteAccount(w http.ResponseWriter, r *http.Request) {
	a.db.Exec(`DELETE FROM accounts WHERE plaid_account_id=? AND item_id IS NULL`, r.FormValue("id"))
	http.Redirect(w, r, "/settings#banks", http.StatusSeeOther)
}
