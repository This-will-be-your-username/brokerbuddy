package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
)

//go:embed static
var staticFS embed.FS

//go:embed templates/*.html
var tplFS embed.FS

type app struct {
	db    *sql.DB
	plaid *plaidClient
	vault *vault
	tpl   *template.Template
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	db, err := openDB(env("DB_PATH", "brokerbuddy.db"))
	if err != nil {
		log.Fatal(err)
	}
	secret := os.Getenv("APP_SECRET")
	if secret == "" {
		log.Fatal("APP_SECRET is required (any long random string; encrypts bank tokens at rest)")
	}
	if err := recategorizeAll(db); err != nil { // move existing data onto the current category set
		log.Println("recategorize:", err)
	}
	v, err := newVault(secret)
	if err != nil {
		log.Fatal(err)
	}
	a := &app{db: db, vault: v, tpl: parseTemplates()}
	if id, sec := os.Getenv("PLAID_CLIENT_ID"), os.Getenv("PLAID_SECRET"); id != "" && sec != "" {
		a.plaid = newPlaid(env("PLAID_ENV", "sandbox"), id, sec)
	} else {
		log.Println("PLAID_CLIENT_ID / PLAID_SECRET not set: bank linking disabled")
	}

	serve(sameOrigin(a.routes()))
}

func parseTemplates() *template.Template {
	return template.Must(template.New("").Funcs(template.FuncMap{
		"neg":  func(f float64) float64 { return -f },
		"list": func(v ...int) []int { return v },
		"pct": func(f, max float64) float64 {
			if max <= 0 {
				return 0
			}
			return f / max * 100
		},
	}).ParseFS(tplFS, "templates/*.html"))
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.dashboard)
	mux.HandleFunc("GET /transactions", a.index)
	mux.HandleFunc("GET /charts", a.chartsPage)
	mux.HandleFunc("GET /api/transactions", a.apiTransactions)
	mux.HandleFunc("GET /mortgage", a.mortgagePage)
	mux.HandleFunc("POST /api/mortgage/calc", a.mortgageCalc)
	mux.HandleFunc("GET /api/mortgage/rates", a.ratesList)
	mux.HandleFunc("POST /api/mortgage/rates/refresh", a.ratesRefresh)
	mux.HandleFunc("GET /api/mortgage/rates/status", a.ratesStatus)
	mux.Handle("GET /static/", http.FileServerFS(staticFS))
	mux.HandleFunc("POST /link/token", a.linkToken)
	mux.HandleFunc("POST /link/exchange", a.linkExchange)
	mux.HandleFunc("POST /sync", a.syncAll)
	mux.HandleFunc("POST /txn/category", a.setCategory)
	mux.HandleFunc("POST /rules", a.addRule)
	mux.HandleFunc("POST /rules/delete", a.deleteRule)
	mux.HandleFunc("POST /items/delete", a.deleteItem)
	mux.HandleFunc("POST /lookup", a.lookupUnknown)
	mux.HandleFunc("POST /lookup/delete", a.lookupDelete)
	mux.HandleFunc("POST /recategorize", a.recategorize)
	mux.HandleFunc("GET /report", a.reportPage)
	mux.HandleFunc("GET /report.csv", a.reportCSV)
	mux.HandleFunc("GET /import", a.importPage)
	mux.HandleFunc("POST /import/upload", a.importUpload)
	mux.HandleFunc("POST /import/run", a.importRun)
	mux.HandleFunc("POST /accounts/delete", a.deleteAccount)

	return mux
}

// sameOrigin is CSRF protection for state-changing requests.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			o := r.Header.Get("Origin")
			if u, err := url.Parse(o); o == "" || err != nil || u.Host != r.Host {
				http.Error(w, "cross-origin request blocked", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func fail(w http.ResponseWriter, err error, code int) {
	log.Println("error:", err)
	http.Error(w, err.Error(), code)
}

type txnRow struct {
	ID, Date, Name, Account, Category, Source string
	Amount                                    float64
	Manual                                    bool
}
type catTotal struct {
	Category string
	Total    float64
}
type itemView struct {
	ID          int
	Institution string
	Accounts    []string
}
type ruleView struct {
	ID                int
	Pattern, Category string
}

func (a *app) index(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cat, search := q.Get("category"), q.Get("q")

	where, args := []string{"1=1"}, []any{}
	if cat != "" {
		where = append(where, "t.category=?")
		args = append(args, cat)
	}
	if search != "" {
		where = append(where, "(t.name LIKE ? OR t.merchant LIKE ?)")
		args = append(args, "%"+search+"%", "%"+search+"%")
	}
	W := strings.Join(where, " AND ")

	var txns []txnRow
	rows, err := a.db.Query(`SELECT t.plaid_txn_id,t.date,t.name,a.name||CASE WHEN a.mask<>'' THEN ' ••'||a.mask ELSE '' END,t.category,t.amount,t.manual,t.cat_source
		FROM transactions t JOIN accounts a ON a.plaid_account_id=t.account_id
		WHERE `+W+` ORDER BY t.date DESC, t.plaid_txn_id LIMIT 500`, args...)
	if err != nil {
		fail(w, err, 500)
		return
	}
	for rows.Next() {
		var t txnRow
		rows.Scan(&t.ID, &t.Date, &t.Name, &t.Account, &t.Category, &t.Amount, &t.Manual, &t.Source)
		txns = append(txns, t)
	}
	rows.Close()

	// Spending totals per category (outflows only, transfers/income excluded) for the filtered set.
	var totals []catTotal
	rows, err = a.db.Query(`SELECT t.category, SUM(t.amount) FROM transactions t
		WHERE `+W+` AND t.amount>0 AND t.category NOT IN ('Transfers','Savings & Investments','Salary & Wages','Government Benefits','Rental Income','Other Income')
		GROUP BY t.category ORDER BY 2 DESC`, args...)
	if err == nil {
		for rows.Next() {
			var c catTotal
			rows.Scan(&c.Category, &c.Total)
			totals = append(totals, c)
		}
		rows.Close()
	}

	var items []itemView
	rows, err = a.db.Query(`SELECT i.id,i.institution,COALESCE(group_concat(a.name||CASE WHEN a.mask<>'' THEN ' ••'||a.mask ELSE '' END,', '),'') FROM items i
		LEFT JOIN accounts a ON a.item_id=i.id GROUP BY i.id`)
	if err == nil {
		for rows.Next() {
			var iv itemView
			var accts string
			rows.Scan(&iv.ID, &iv.Institution, &accts)
			iv.Accounts = []string{accts}
			items = append(items, iv)
		}
		rows.Close()
	}

	var rules []ruleView
	rows, err = a.db.Query(`SELECT id,pattern,category FROM rules ORDER BY id DESC`)
	if err == nil {
		for rows.Next() {
			var rv ruleView
			rows.Scan(&rv.ID, &rv.Pattern, &rv.Category)
			rules = append(rules, rv)
		}
		rows.Close()
	}

	catList := allCategories()

	a.tpl.ExecuteTemplate(w, "index.html", map[string]any{
		"Txns": txns, "Totals": totals, "Items": items, "Rules": rules, "Cats": catList,
		"Max": maxTotal(totals), "Category": cat, "Q": search, "PlaidReady": a.plaid != nil,
		"Flash": q.Get("msg"), "Manual": a.manualAccounts(), "Lookups": a.lookupList(),
	})
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func (a *app) linkToken(w http.ResponseWriter, r *http.Request) {
	if a.plaid == nil {
		fail(w, errNoPlaid, 503)
		return
	}
	t, err := a.plaid.linkToken()
	if err != nil {
		fail(w, err, 502)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"link_token": t})
}

func (a *app) linkExchange(w http.ResponseWriter, r *http.Request) {
	if a.plaid == nil {
		fail(w, errNoPlaid, 503)
		return
	}
	var in struct {
		PublicToken string `json:"public_token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil || in.PublicToken == "" {
		fail(w, errBad, 400)
		return
	}
	ex, err := a.plaid.exchange(in.PublicToken)
	if err != nil {
		fail(w, err, 502)
		return
	}
	enc, err := a.vault.seal(ex.AccessToken)
	if err != nil {
		fail(w, err, 500)
		return
	}
	res, err := a.db.Exec(`INSERT INTO items(plaid_item_id,access_token,institution) VALUES(?,?,?)
		ON CONFLICT(plaid_item_id) DO UPDATE SET access_token=excluded.access_token`,
		ex.ItemID, enc, a.plaid.institution(ex.AccessToken))
	if err != nil {
		fail(w, err, 500)
		return
	}
	_ = res
	// Initial pull happens in the background of this request; Plaid may need a moment, so
	// a failure here is non-fatal — the user can hit "Sync now".
	if err := a.syncItemByPlaidID(ex.ItemID); err != nil {
		log.Println("initial sync:", err)
	}
	w.Write([]byte(`{"ok":true}`))
}

func (a *app) syncAll(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.Query(`SELECT plaid_item_id FROM items`)
	if err != nil {
		fail(w, err, 500)
		return
	}
	var ids []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		ids = append(ids, s)
	}
	rows.Close()
	msg := "Synced"
	for _, id := range ids {
		if err := a.syncItemByPlaidID(id); err != nil {
			log.Println("sync:", err)
			msg = "Sync error: " + err.Error()
		}
	}
	http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (a *app) setCategory(w http.ResponseWriter, r *http.Request) {
	id, cat := r.FormValue("id"), strings.TrimSpace(r.FormValue("category"))
	if id == "" || cat == "" {
		fail(w, errBad, 400)
		return
	}
	var before, after int
	countLearned := func(n *int) {
		a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE manual=0 AND cat_source='learned' AND category=?`, cat).Scan(n)
	}
	countLearned(&before)
	if _, err := a.db.Exec(`UPDATE transactions SET category=?, manual=1 WHERE plaid_txn_id=?`, cat, id); err != nil {
		fail(w, err, 500)
		return
	}
	if r.FormValue("make_rule") == "on" {
		var name, merchant string
		a.db.QueryRow(`SELECT name, merchant FROM transactions WHERE plaid_txn_id=?`, id).Scan(&name, &merchant)
		pat := merchant
		if pat == "" {
			pat = name
		}
		if pat != "" {
			a.db.Exec(`INSERT INTO rules(pattern,category) VALUES(?,?)`, pat, cat)
		}
	}
	// Every manual edit teaches the classifier: re-run it so similar transactions follow.
	if err := recategorizeAll(a.db); err != nil {
		log.Println("recategorize:", err)
	}
	countLearned(&after)
	dest := "/transactions"
	if ref, err := url.Parse(r.Header.Get("Referer")); err == nil && ref.Path != "" {
		q := ref.Query()
		q.Del("msg")
		if after > before {
			q.Set("msg", fmt.Sprintf("Also applied to %d similar transactions", after-before))
		}
		dest = ref.Path + "?" + q.Encode()
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (a *app) addRule(w http.ResponseWriter, r *http.Request) {
	p, c := strings.TrimSpace(r.FormValue("pattern")), strings.TrimSpace(r.FormValue("category"))
	if p == "" || c == "" {
		fail(w, errBad, 400)
		return
	}
	if _, err := a.db.Exec(`INSERT INTO rules(pattern,category) VALUES(?,?)`, p, c); err != nil {
		fail(w, err, 500)
		return
	}
	recategorizeAll(a.db)
	http.Redirect(w, r, "/transactions", http.StatusSeeOther)
}

func (a *app) deleteRule(w http.ResponseWriter, r *http.Request) {
	a.db.Exec(`DELETE FROM rules WHERE id=?`, r.FormValue("id"))
	recategorizeAll(a.db)
	http.Redirect(w, r, "/transactions", http.StatusSeeOther)
}

func (a *app) deleteItem(w http.ResponseWriter, r *http.Request) {
	var enc string
	if err := a.db.QueryRow(`SELECT access_token FROM items WHERE id=?`, r.FormValue("id")).Scan(&enc); err == nil && a.plaid != nil {
		if tok, err := a.vault.open(enc); err == nil {
			if err := a.plaid.remove(tok); err != nil {
				log.Println("plaid remove:", err)
			}
		}
	}
	a.db.Exec(`DELETE FROM items WHERE id=?`, r.FormValue("id"))
	http.Redirect(w, r, "/transactions", http.StatusSeeOther)
}

func maxTotal(t []catTotal) float64 {
	m := 0.0
	for _, c := range t {
		if c.Total > m {
			m = c.Total
		}
	}
	return m
}
