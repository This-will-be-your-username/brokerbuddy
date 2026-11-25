package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// version is set at build time: go build -ldflags "-X main.version=v1.2.3"
var version = "dev"

//go:embed static
var staticFS embed.FS

//go:embed templates/*.html
var tplFS embed.FS

type app struct {
	db      *sql.DB
	plaid   atomic.Pointer[plaidClient] // replaced live when Plaid settings change
	vault   *vault
	tpl     *template.Template
	dbPath  string
	keyPath string
	net     *netManager
	sess    *sessionStore
	limiter *loginLimiter
	tg      *telegramBot
}

// pc returns the Plaid client, or nil when Plaid isn't configured.
func (a *app) pc() *plaidClient { return a.plaid.Load() }

func main() {
	// The only startup options. Everything else is configured in the web interface (Settings).
	dbPath := flag.String("db", "brokerbuddy.db", "path to the SQLite database file")
	addr := flag.String("addr", "127.0.0.1:8443", "local address for the web interface")
	showVersion := flag.Bool("version", false, "print the version and exit")
	demo := flag.Bool("demo", false, "fill an empty database with a year of fictional demo transactions (use with -db demo.db)")
	flag.Parse()
	if *showVersion {
		fmt.Println("BrokerBuddy", version)
		return
	}

	a, err := newApp(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	if *demo {
		if n, err := loadDemo(a.db, time.Now()); err != nil {
			log.Fatal(err)
		} else {
			log.Printf("loaded %d fictional demo transactions into %s", n, *dbPath)
		}
	}
	a.net = newNetManager(a, a.routes(), *addr)
	a.net.Start()
	a.tg.Reload() // starts the Telegram bot if it is configured and enabled
	log.Printf("BrokerBuddy is running: open https://%s in your browser (configure everything under Settings)", *addr)
	select {}
}

func newApp(dbPath string) (*app, error) {
	db, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}
	keyPath := dbPath + ".key"
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}
	v, err := newVault(key)
	if err != nil {
		return nil, err
	}
	a := &app{db: db, vault: v, tpl: parseTemplates(), dbPath: dbPath, keyPath: keyPath, sess: newSessionStore(), limiter: newLoginLimiter()}
	a.tg = newTelegramBot(a)
	a.reloadPlaid()
	if n, err := cleanStoredNames(db); err != nil {
		log.Println("clean names:", err)
	} else if n > 0 {
		log.Printf("removed page-footer text from %d stored descriptions", n)
	}
	if err := recategorizeAll(db); err != nil { // move existing data onto the current category set
		log.Println("recategorize:", err)
	}
	return a, nil
}

func parseTemplates() *template.Template {
	return template.Must(template.New("").Funcs(template.FuncMap{
		"neg":   func(f float64) float64 { return -f },
		"list":  func(v ...int) []int { return v },
		"money": fmtMoney,
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
	mux.HandleFunc("POST /demo/load", a.demoLoad)
	mux.HandleFunc("POST /demo/remove", a.demoRemove)
	mux.HandleFunc("POST /api/suggest", a.suggestPage)
	mux.HandleFunc("POST /lookup/delete", a.lookupDelete)
	mux.HandleFunc("POST /recategorize", a.recategorize)
	mux.HandleFunc("POST /api/reprocess", a.reprocessAPI)
	mux.HandleFunc("GET /report", a.reportPage)
	mux.HandleFunc("GET /report.csv", a.reportCSV)
	mux.HandleFunc("GET /import", a.importPage)
	mux.HandleFunc("POST /import/upload", a.importUpload)
	mux.HandleFunc("POST /import/run", a.importRun)
	mux.HandleFunc("POST /accounts/delete", a.deleteAccount)
	mux.HandleFunc("POST /accounts/flag", a.flagAccount)
	mux.HandleFunc("POST /settings/cards", a.saveCardSettings)
	mux.HandleFunc("POST /txn/ignore", a.setIgnoreOverride)
	mux.HandleFunc("POST /txn/flip", a.flipDirection)
	mux.HandleFunc("POST /txn/reimb", a.setReimb)
	mux.HandleFunc("POST /settings/reimb", a.saveReimbSettings)
	// settings & sign-in
	mux.HandleFunc("GET /settings", a.settingsPage)
	mux.HandleFunc("POST /settings/plaid", a.savePlaid)
	mux.HandleFunc("POST /settings/telegram", a.saveTelegram)
	mux.HandleFunc("POST /settings/telegram/ask", a.telegramAskNow)
	mux.HandleFunc("POST /settings/password", a.savePassword)
	mux.HandleFunc("POST /settings/password/remove", a.removePassword)
	mux.HandleFunc("POST /settings/network", a.saveNetwork)
	mux.HandleFunc("GET /settings/export", a.exportDownload)
	mux.HandleFunc("POST /settings/import", a.importUploadBackup)
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.loginSubmit)
	mux.HandleFunc("POST /logout", a.logout)

	return secureHeaders(sameOrigin(a.auth(mux)))
}

// sameOrigin is CSRF protection for state-changing requests.
//
// Browsers label every request with Sec-Fetch-Site, a header page scripts cannot forge, so that is
// trusted first. Origin is not reliable on its own: a plain form post can legitimately carry
// "Origin: null" (for instance under a no-referrer policy). Clients without Sec-Fetch-Site fall
// back to checking that Origin, or failing that Referer, names this host.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !requestIsSameOrigin(r) {
			http.Error(w, "cross-origin request blocked", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestIsSameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none": // "none" = typed in the address bar / bookmark, never another site
		return true
	case "":
		// older client or a non-browser: fall through to the header checks
	default: // same-site (a sibling subdomain) or cross-site
		return false
	}
	for _, h := range []string{"Origin", "Referer"} {
		if v := r.Header.Get(h); v != "" && v != "null" {
			u, err := url.Parse(v)
			return err == nil && u.Host == r.Host
		}
	}
	return false
}

func fail(w http.ResponseWriter, err error, code int) {
	log.Println("error:", err)
	http.Error(w, err.Error(), code)
}

type txnRow struct {
	ID, Date, Name, Account, Category, Source string
	Amount                                    float64
	Manual                                    bool
	Conf                                      int
	Ignored                                   bool // a credit card repayment that is left out of the analysis
	Override                                  int  // -1 = counted although it would be ignored
	IgnoreWhy                                 string
	Reimb                                     bool // a repayment from another person: its category is the spending it offsets
	ReimbOverride                             int
	ReimbHeld                                 bool   // flagged, but not yet matched to a spending category
	CanReimb                                  bool   // a credit that could be flagged as a repayment
	Level, Why                                string // confidence level (confirmed/high/medium/low/unknown) and the reason
	Flipped                                   bool   // debit/credit was reversed by the user
}

type confChip struct {
	Label, Level, URL string
	Count             int
	Active            bool
}

// confClause turns the ?confidence= filter into SQL.
func confClause(f string) string {
	switch f {
	case "confirmed":
		return "t.manual=1"
	case "high":
		return "t.manual=0 AND t.confidence>=80 AND t.category<>'Uncategorized'"
	case "medium":
		return "t.manual=0 AND t.confidence BETWEEN 50 AND 79 AND t.category<>'Uncategorized'"
	case "low":
		return "t.manual=0 AND t.ignored=0 AND t.confidence BETWEEN 1 AND 49 AND t.category<>'Uncategorized'" // ignored rows never need a category fixed
	case "unknown":
		return "t.manual=0 AND t.ignored=0 AND (t.confidence=0 OR t.category='Uncategorized')"
	case "review": // everything doubtful: low confidence or no category
		return "t.manual=0 AND t.ignored=0 AND (t.confidence<50 OR t.category='Uncategorized')"
	}
	return ""
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

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func (a *app) linkToken(w http.ResponseWriter, r *http.Request) {
	if a.pc() == nil {
		fail(w, errNoPlaid, 503)
		return
	}
	t, err := a.pc().linkToken()
	if err != nil {
		fail(w, err, 502)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"link_token": t})
}

func (a *app) linkExchange(w http.ResponseWriter, r *http.Request) {
	if a.pc() == nil {
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
	ex, err := a.pc().exchange(in.PublicToken)
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
		ex.ItemID, enc, a.pc().institution(ex.AccessToken))
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
	applied, fixed, err := a.categorizeTxn(id, cat, r.FormValue("make_rule") == "on")
	if err != nil {
		fail(w, err, 500)
		return
	}
	dest := "/transactions"
	if ref, err := url.Parse(r.Header.Get("Referer")); err == nil && ref.Path != "" {
		q := ref.Query()
		q.Del("msg")
		if applied > 0 {
			msg := fmt.Sprintf("Also applied “%s” to %d other transaction(s) from the same or a similar merchant", cat, applied)
			if fixed > 0 {
				msg += fmt.Sprintf(", %d of them were unsure", fixed)
			}
			q.Set("msg", msg+".")
		}
		dest = ref.Path + "?" + q.Encode()
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// categorizeTxn confirms a category for one transaction (as if the user edited it), then looks
// through the history so the same or a similar merchant follows. It returns how many other
// transactions took the category, and how many of those were previously unsure. Shared by the
// web interface and the Telegram bot.
func (a *app) categorizeTxn(id, cat string, makeRule bool) (applied, fixed int, err error) {
	if _, err = a.db.Exec(`UPDATE transactions SET category=?, manual=1, confidence=100, cat_source='manual',
		edited_seq=(SELECT COALESCE(MAX(edited_seq),0)+1 FROM transactions) WHERE plaid_txn_id=?`, cat, id); err != nil {
		return
	}
	// Count what the history lookup below changes (the row just edited no longer counts), so the user is told.
	count := func(sql string, args ...any) (n int) { a.db.QueryRow(sql, args...).Scan(&n); return }
	beforeCat := count(`SELECT COUNT(*) FROM transactions WHERE manual=0 AND category=?`, cat)
	beforeUnknown := count(`SELECT COUNT(*) FROM transactions WHERE manual=0 AND (confidence<50 OR category='Uncategorized')`)
	if makeRule {
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
	// Look through the transaction history: other transactions from the same merchant, whether
	// uncategorised or low confidence, take this category (and anything close to it follows).
	// The edit just made is now a confirmed example the classifier learns from.
	a.applyToSameMerchant(id, cat)
	if _, rerr := recategorize(a.db); rerr != nil {
		log.Println("recategorize:", rerr)
	}
	applied = count(`SELECT COUNT(*) FROM transactions WHERE manual=0 AND category=?`, cat) - beforeCat
	fixed = beforeUnknown - count(`SELECT COUNT(*) FROM transactions WHERE manual=0 AND (confidence<50 OR category='Uncategorized')`)
	return
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
	n, _ := recategorize(a.db)
	msg := "Rule added."
	if n > 0 {
		msg = fmt.Sprintf("Rule added and applied to %d transaction(s).", n)
	}
	http.Redirect(w, r, "/settings?msg="+url.QueryEscape(msg)+"#categorisation", http.StatusSeeOther)
}

func (a *app) deleteRule(w http.ResponseWriter, r *http.Request) {
	a.db.Exec(`DELETE FROM rules WHERE id=?`, r.FormValue("id"))
	recategorizeAll(a.db)
	http.Redirect(w, r, "/settings#categorisation", http.StatusSeeOther)
}

func (a *app) deleteItem(w http.ResponseWriter, r *http.Request) {
	var enc string
	if err := a.db.QueryRow(`SELECT access_token FROM items WHERE id=?`, r.FormValue("id")).Scan(&enc); err == nil && a.pc() != nil {
		if tok, err := a.vault.open(enc); err == nil {
			if err := a.pc().remove(tok); err != nil {
				log.Println("plaid remove:", err)
			}
		}
	}
	a.db.Exec(`DELETE FROM items WHERE id=?`, r.FormValue("id"))
	http.Redirect(w, r, "/settings#banks", http.StatusSeeOther)
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
