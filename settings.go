package main

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ---- key file --------------------------------------------------------------

// loadOrCreateKey returns the key used to encrypt secrets stored in the database (bank access
// tokens, the Plaid secret, private keys). It is generated on first run and kept in a file next
// to the database, so nothing has to be configured. Keep the two files together: without the
// key file, stored secrets cannot be decrypted (reconnect banks and re-enter the Plaid secret).
func loadOrCreateKey(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if k := strings.TrimSpace(string(b)); len(k) >= 32 {
			return k, nil
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	k := hex.EncodeToString(raw)
	return k, os.WriteFile(path, []byte(k+"\n"), 0o600)
}

// ---- settings store ----------------------------------------------------------

func (a *app) getSetting(k string) string {
	var v string
	if err := a.db.QueryRow(`SELECT value FROM settings WHERE key=?`, k).Scan(&v); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	return v
}

func (a *app) setSetting(k, v string) error {
	if v == "" {
		_, err := a.db.Exec(`DELETE FROM settings WHERE key=?`, k)
		return err
	}
	_, err := a.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, v)
	return err
}

func (a *app) getSecret(k string) string {
	enc := a.getSetting(k)
	if enc == "" {
		return ""
	}
	v, err := a.vault.open(enc)
	if err != nil {
		return ""
	}
	return v
}

func (a *app) setSecret(k, v string) error {
	if v == "" {
		return a.setSetting(k, "")
	}
	enc, err := a.vault.seal(v)
	if err != nil {
		return err
	}
	return a.setSetting(k, enc)
}

// reloadPlaid rebuilds the Plaid client from settings; called at start-up and after saving.
func (a *app) reloadPlaid() {
	id, sec := a.getSetting("plaid_client_id"), a.getSecret("plaid_secret")
	if id == "" || sec == "" {
		a.plaid.Store(nil)
		return
	}
	env := a.getSetting("plaid_env")
	if env != "production" {
		env = "sandbox"
	}
	a.plaid.Store(newPlaid(env, id, sec))
}

// ---- sessions & login --------------------------------------------------------

const sessionCookie = "bb_session"

type sessionStore struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func newSessionStore() *sessionStore { return &sessionStore{m: map[string]time.Time{}} }

func (s *sessionStore) create() string {
	b := make([]byte, 32)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, exp := range s.m { // drop expired
		if now.After(exp) {
			delete(s.m, k)
		}
	}
	s.m[tok] = now.Add(30 * 24 * time.Hour)
	return tok
}

func (s *sessionStore) valid(tok string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[tok]
	return ok && time.Now().Before(exp)
}

func (s *sessionStore) drop(tok string) { s.mu.Lock(); delete(s.m, tok); s.mu.Unlock() }
func (s *sessionStore) clear()          { s.mu.Lock(); s.m = map[string]time.Time{}; s.mu.Unlock() }

// loginLimiter allows a handful of attempts per client address, to slow password guessing.
type loginLimiter struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{m: map[string][]time.Time{}} }

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := time.Now().Add(-5 * time.Minute)
	var keep []time.Time
	for _, t := range l.m[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= 8 {
		l.m[ip] = keep
		return false
	}
	l.m[ip] = append(keep, time.Now())
	return true
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// isLocalHost reports whether the Host header names this computer. Without a password, only such
// requests are served, which also blocks DNS-rebinding attacks from other websites.
func isLocalHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func (a *app) hasPassword() bool { return a.getSetting("password_hash") != "" }

func (a *app) signedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && a.sess.valid(c.Value)
}

func (a *app) startSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: a.sess.create(), Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600})
}

// auth protects every page. With no password set the app is open, but only to this computer.
func (a *app) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/login" || p == "/logout" || strings.HasPrefix(p, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		if !a.hasPassword() {
			if !isLocalHost(r.Host) {
				http.Error(w, "No password is set, so BrokerBuddy only answers on this computer. Open Settings locally and set a password first.", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if a.signedIn(r) {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && !strings.HasPrefix(p, "/api/") {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		http.Error(w, "sign in required", http.StatusUnauthorized)
	})
}

func safeNext(n string) string {
	if strings.HasPrefix(n, "/") && !strings.HasPrefix(n, "//") && !strings.Contains(n, "\\") {
		return n
	}
	return "/"
}

func (a *app) loginPage(w http.ResponseWriter, r *http.Request) {
	if !a.hasPassword() || a.signedIn(r) {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	a.tpl.ExecuteTemplate(w, "login.html", map[string]any{"Next": safeNext(r.URL.Query().Get("next")), "Error": r.URL.Query().Get("err")})
}

func (a *app) loginSubmit(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.FormValue("next"))
	bad := func(msg string) {
		http.Redirect(w, r, "/login?err="+url.QueryEscape(msg)+"&next="+url.QueryEscape(next), http.StatusSeeOther)
	}
	if !a.limiter.allow(clientIP(r)) {
		bad("Too many attempts. Wait a few minutes and try again.")
		return
	}
	hash := a.getSetting("password_hash")
	if hash == "" || bcrypt.CompareHashAndPassword([]byte(hash), []byte(r.FormValue("password"))) != nil {
		bad("Incorrect password.")
		return
	}
	a.startSession(w)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.sess.drop(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ---- settings page -----------------------------------------------------------

func settingsRedirect(w http.ResponseWriter, r *http.Request, anchor, msg string, isErr bool) {
	k := "msg"
	if isErr {
		k = "err"
	}
	http.Redirect(w, r, "/settings?"+k+"="+url.QueryEscape(msg)+"#"+anchor, http.StatusSeeOther)
}

func (a *app) settingsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var items []itemView
	if rows, err := a.db.Query(`SELECT i.id,i.institution,COALESCE(group_concat(a.name||CASE WHEN a.mask<>'' THEN ' ••'||a.mask ELSE '' END,', '),'') FROM items i
		LEFT JOIN accounts a ON a.item_id=i.id GROUP BY i.id`); err == nil {
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
	if rows, err := a.db.Query(`SELECT id,pattern,category FROM rules ORDER BY id DESC`); err == nil {
		for rows.Next() {
			var rv ruleView
			rows.Scan(&rv.ID, &rv.Pattern, &rv.Category)
			rules = append(rules, rv)
		}
		rows.Close()
	}
	env := a.getSetting("plaid_env")
	if env == "" {
		env = "sandbox"
	}
	mode := a.getSetting("net_mode")
	if mode == "" {
		mode = "local"
	}
	addr := a.getSetting("net_addr")
	if addr == "" {
		addr = ":443"
	}
	a.tpl.ExecuteTemplate(w, "settings.html", map[string]any{
		"ReimbEnabled": loadReimbCfg(a.db).enabled, "ReimbMax": strconv.FormatFloat(loadReimbCfg(a.db).max, 'f', -1, 64), "ReimbCount": a.countReimb(),
		"AllAccounts": a.accountRows(), "CardElsewhere": loadCardCtx(a.db).elsewhere, "IgnoredTotal": a.countIgnored(""),
		"Flash": q.Get("msg"), "Error": q.Get("err"),
		"PlaidClientID": a.getSetting("plaid_client_id"), "PlaidSecretSet": a.getSetting("plaid_secret") != "",
		"PlaidEnv": env, "PlaidReady": a.pc() != nil,
		"Items": items, "Manual": a.manualAccounts(), "Rules": rules, "Lookups": a.lookupList(), "Cats": allCategories(),
		"HasPassword": a.hasPassword(),
		"NetMode":     mode, "NetDomain": a.getSetting("net_domain"), "NetEmail": a.getSetting("net_email"), "NetAddr": addr,
		"NetStaging": a.getSetting("net_staging") == "1", "CertSet": a.getSetting("net_cert") != "",
		"Status": a.net.Status(), "LocalAddr": a.net.localAddr, "DBPath": a.dbPath, "KeyPath": a.keyPath,
	})
}

func (a *app) savePlaid(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("remove") == "on" {
		for _, k := range []string{"plaid_client_id", "plaid_secret", "plaid_env"} {
			a.setSetting(k, "")
		}
		a.reloadPlaid()
		settingsRedirect(w, r, "banks", "Plaid credentials removed.", false)
		return
	}
	id := strings.TrimSpace(r.FormValue("client_id"))
	sec := strings.TrimSpace(r.FormValue("secret"))
	env := r.FormValue("env")
	if env != "production" {
		env = "sandbox"
	}
	if id == "" {
		settingsRedirect(w, r, "banks", "Enter your Plaid client ID.", true)
		return
	}
	if sec == "" && a.getSetting("plaid_secret") == "" {
		settingsRedirect(w, r, "banks", "Enter your Plaid secret.", true)
		return
	}
	if err := a.setSetting("plaid_client_id", id); err != nil {
		settingsRedirect(w, r, "banks", err.Error(), true)
		return
	}
	if sec != "" { // blank means "keep the saved secret"
		if err := a.setSecret("plaid_secret", sec); err != nil {
			settingsRedirect(w, r, "banks", err.Error(), true)
			return
		}
	}
	a.setSetting("plaid_env", env)
	a.reloadPlaid()
	if _, err := a.pc().linkToken(); err != nil { // quick credential check
		settingsRedirect(w, r, "banks", "Saved, but Plaid rejected the credentials: "+err.Error(), true)
		return
	}
	settingsRedirect(w, r, "banks", "Plaid connected ("+env+"). You can now connect a bank from Transactions.", false)
}

func (a *app) savePassword(w http.ResponseWriter, r *http.Request) {
	cur, pw, again := r.FormValue("current"), r.FormValue("password"), r.FormValue("again")
	if hash := a.getSetting("password_hash"); hash != "" && bcrypt.CompareHashAndPassword([]byte(hash), []byte(cur)) != nil {
		settingsRedirect(w, r, "security", "Current password is incorrect.", true)
		return
	}
	switch {
	case len(pw) < 8:
		settingsRedirect(w, r, "security", "Use at least 8 characters.", true)
		return
	case subtle.ConstantTimeCompare([]byte(pw), []byte(again)) != 1:
		settingsRedirect(w, r, "security", "The two new passwords don't match.", true)
		return
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		settingsRedirect(w, r, "security", err.Error(), true)
		return
	}
	a.setSetting("password_hash", string(h))
	a.sess.clear() // everyone else is signed out; this browser stays in
	a.startSession(w)
	settingsRedirect(w, r, "security", "Password saved. You are signed in.", false)
}

func (a *app) removePassword(w http.ResponseWriter, r *http.Request) {
	hash := a.getSetting("password_hash")
	if hash == "" {
		settingsRedirect(w, r, "security", "No password is set.", true)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(r.FormValue("current"))) != nil {
		settingsRedirect(w, r, "security", "Current password is incorrect.", true)
		return
	}
	if m := a.getSetting("net_mode"); m != "" && m != "local" {
		settingsRedirect(w, r, "security", "A password is required while internet access is on. Switch Access to “This computer only” first.", true)
		return
	}
	a.setSetting("password_hash", "")
	a.sess.clear()
	settingsRedirect(w, r, "security", "Password removed. BrokerBuddy now only answers on this computer.", false)
}

var hostnameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)

func (a *app) saveNetwork(w http.ResponseWriter, r *http.Request) {
	mode := r.FormValue("mode")
	domain := strings.ToLower(strings.TrimSpace(r.FormValue("domain")))
	email := strings.TrimSpace(r.FormValue("email"))
	addr := strings.TrimSpace(r.FormValue("addr"))
	cert, key := strings.TrimSpace(r.FormValue("cert")), strings.TrimSpace(r.FormValue("key"))
	fail := func(msg string) { settingsRedirect(w, r, "access", msg, true) }

	switch mode {
	case "local":
	case "letsencrypt":
		if !a.hasPassword() {
			fail("Set a password first (Security): the app will be reachable from the internet.")
			return
		}
		if !hostnameRe.MatchString(domain) {
			fail("Enter a valid domain name, e.g. money.example.com. Let's Encrypt can't issue certificates for IP addresses or localhost.")
			return
		}
	case "custom":
		if !a.hasPassword() {
			fail("Set a password first (Security): the app will be reachable from other devices.")
			return
		}
		if addr == "" {
			addr = ":443"
		}
		if _, _, err := net.SplitHostPort(addr); err != nil {
			fail("Listen address should look like :443 or 0.0.0.0:8443.")
			return
		}
		if cert == "" {
			cert = a.getSecret("net_cert")
		}
		if key == "" {
			key = a.getSecret("net_key")
		}
		if _, err := parseKeyPair(cert, key); err != nil {
			fail("That certificate and private key don't match or aren't valid PEM: " + err.Error())
			return
		}
	default:
		fail("Choose how BrokerBuddy should be reached.")
		return
	}
	a.setSetting("net_mode", mode)
	if mode == "letsencrypt" {
		a.setSetting("net_domain", domain)
		a.setSetting("net_email", email)
		if r.FormValue("staging") == "on" {
			a.setSetting("net_staging", "1")
		} else {
			a.setSetting("net_staging", "")
		}
	}
	if mode == "custom" {
		a.setSetting("net_addr", addr)
		a.setSecret("net_cert", cert)
		a.setSecret("net_key", key)
	}
	go func() { time.Sleep(400 * time.Millisecond); a.net.Reapply() }() // after this response is sent
	msg := "Saved. Applying…  Check the status below in a few seconds."
	if mode == "local" {
		msg = "Saved. Internet access turned off."
	}
	settingsRedirect(w, r, "access", msg, false)
}

// secureHeaders adds standard hardening headers.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin") // nothing leaks to other sites, but same-site form posts keep a real Origin
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
