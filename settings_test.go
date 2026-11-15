package main

import (
	"bytes"
	"io"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func testApp(t *testing.T) *app {
	t.Helper()
	a, err := newApp(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	a.net = newNetManager(a, nil, "127.0.0.1:0")
	t.Cleanup(func() { a.db.Close() }) // Windows can't delete an open database file
	return a
}

// client posts same-origin forms like a browser would and keeps cookies.
type testClient struct {
	t    *testing.T
	srv  *httptest.Server
	http *http.Client
}

func newClient(t *testing.T, a *app) *testClient {
	srv := httptest.NewServer(a.routes())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &testClient{t, srv, c}
}

func (c *testClient) get(path string) *http.Response {
	resp, err := c.http.Get(c.srv.URL + path)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp
}

func (c *testClient) post(path string, form url.Values) *http.Response {
	req, _ := http.NewRequest("POST", c.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", c.srv.URL)
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp
}

func TestKeyFileCreatedOnceAndReused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k")
	k1, err := loadOrCreateKey(p)
	if err != nil || len(k1) < 32 {
		t.Fatal(err, k1)
	}
	if k2, _ := loadOrCreateKey(p); k2 != k1 {
		t.Error("key must be stable across runs")
	}
	if _, err := os.Stat(p); err != nil {
		t.Error(err)
	}
}

func TestSecretsAreEncryptedAtRest(t *testing.T) {
	a := testApp(t)
	if err := a.setSecret("plaid_secret", "super-secret-value"); err != nil {
		t.Fatal(err)
	}
	if raw := a.getSetting("plaid_secret"); raw == "" || strings.Contains(raw, "super-secret") {
		t.Errorf("stored value must be ciphertext, got %q", raw)
	}
	if got := a.getSecret("plaid_secret"); got != "super-secret-value" {
		t.Errorf("round trip: %q", got)
	}
}

func TestOpenOnlyOnLocalhostWithoutPassword(t *testing.T) {
	a := testApp(t)
	h := a.routes()
	for host, want := range map[string]int{"127.0.0.1:8443": 200, "localhost:8443": 200, "[::1]:8443": 200, "evil.example.com": 403, "192.168.1.20:8443": 403} {
		r := httptest.NewRequest("GET", "/charts", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("host %q: got %d want %d", host, w.Code, want)
		}
	}
}

func TestPasswordFlow(t *testing.T) {
	a := testApp(t)
	c := newClient(t, a)

	// set a password (no current password needed the first time)
	if resp := c.post("/settings/password", url.Values{"password": {"short"}, "again": {"short"}}); !strings.Contains(resp.Header.Get("Location"), "err=") {
		t.Error("short password must be rejected")
	}
	if resp := c.post("/settings/password", url.Values{"password": {"correct horse"}, "again": {"different one"}}); !strings.Contains(resp.Header.Get("Location"), "err=") {
		t.Error("mismatch must be rejected")
	}
	if resp := c.post("/settings/password", url.Values{"password": {"correct horse"}, "again": {"correct horse"}}); strings.Contains(resp.Header.Get("Location"), "err=") {
		t.Fatalf("valid password rejected: %s", resp.Header.Get("Location"))
	}
	if h := a.getSetting("password_hash"); h == "" || strings.Contains(h, "correct horse") {
		t.Error("password must be stored hashed")
	}
	// the browser that set it is signed in
	if resp := c.get("/charts"); resp.StatusCode != 200 {
		t.Errorf("setter should be signed in, got %d", resp.StatusCode)
	}

	// a fresh browser is sent to the login page; API calls get 401
	other := newClient(t, a)
	if resp := other.get("/charts"); resp.StatusCode != 303 || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Errorf("expected redirect to login, got %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := other.get("/api/transactions"); resp.StatusCode != 401 {
		t.Errorf("api should answer 401, got %d", resp.StatusCode)
	}
	if resp := other.post("/login", url.Values{"password": {"wrong"}}); !strings.Contains(resp.Header.Get("Location"), "err=") {
		t.Error("wrong password must fail")
	}
	if resp := other.post("/login", url.Values{"password": {"correct horse"}, "next": {"/charts"}}); resp.StatusCode != 303 || resp.Header.Get("Location") != "/charts" {
		t.Errorf("login: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := other.get("/charts"); resp.StatusCode != 200 {
		t.Errorf("after login: %d", resp.StatusCode)
	}

	// changing the password signs everyone else out
	c.post("/settings/password", url.Values{"current": {"correct horse"}, "password": {"another phrase"}, "again": {"another phrase"}})
	if resp := other.get("/charts"); resp.StatusCode != 303 {
		t.Errorf("other session should be signed out, got %d", resp.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	a := testApp(t)
	a.setSetting("password_hash", "x")
	l := a.limiter
	ok := 0
	for i := 0; i < 12; i++ {
		if l.allow("1.2.3.4") {
			ok++
		}
	}
	if ok != 8 {
		t.Errorf("expected 8 attempts allowed, got %d", ok)
	}
	if !l.allow("5.6.7.8") {
		t.Error("limit is per address")
	}
}

func TestCrossOriginPostBlocked(t *testing.T) {
	a := testApp(t)
	c := newClient(t, a)
	req, _ := http.NewRequest("POST", c.srv.URL+"/rules", strings.NewReader("pattern=x&category=Groceries"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example.com")
	resp, _ := c.http.Do(req)
	if resp.StatusCode != 403 {
		t.Errorf("got %d", resp.StatusCode)
	}
}

func TestNetworkSettingsValidation(t *testing.T) {
	a := testApp(t)
	c := newClient(t, a)
	loc := func(r *http.Response) string { return r.Header.Get("Location") }

	if !strings.Contains(loc(c.post("/settings/network", url.Values{"mode": {"letsencrypt"}, "domain": {"money.example.com"}})), "err=") {
		t.Error("internet access must require a password first")
	}
	c.post("/settings/password", url.Values{"password": {"correct horse"}, "again": {"correct horse"}})
	if !strings.Contains(loc(c.post("/settings/network", url.Values{"mode": {"letsencrypt"}, "domain": {"127.0.0.1"}})), "err=") {
		t.Error("an IP address is not a valid Let's Encrypt domain")
	}
	if !strings.Contains(loc(c.post("/settings/network", url.Values{"mode": {"letsencrypt"}, "domain": {"localhost"}})), "err=") {
		t.Error("localhost is not a valid Let's Encrypt domain")
	}
	if !strings.Contains(loc(c.post("/settings/network", url.Values{"mode": {"custom"}, "addr": {":443"}, "cert": {"nope"}, "key": {"nope"}})), "err=") {
		t.Error("garbage certificate must be rejected")
	}
	if strings.Contains(loc(c.post("/settings/network", url.Values{"mode": {"letsencrypt"}, "domain": {"money.example.com"}, "email": {"me@example.com"}})), "err=") {
		t.Error("valid domain rejected")
	}
	if a.getSetting("net_mode") != "letsencrypt" || a.getSetting("net_domain") != "money.example.com" {
		t.Error("settings not saved")
	}
	// the password can't be removed while internet access is on
	if !strings.Contains(loc(c.post("/settings/password/remove", url.Values{"current": {"correct horse"}})), "err=") {
		t.Error("password removal must be blocked while internet access is on")
	}
}

func TestPlaidSettingsStoredEncryptedAndHotReloaded(t *testing.T) {
	a := testApp(t)
	if a.pc() != nil {
		t.Fatal("no Plaid client expected before configuration")
	}
	a.setSetting("plaid_client_id", "id123")
	a.setSecret("plaid_secret", "sec456")
	a.setSetting("plaid_env", "sandbox")
	a.reloadPlaid()
	if p := a.pc(); p == nil || p.id != "id123" || p.secret != "sec456" || !strings.Contains(p.base, "sandbox") {
		t.Errorf("%+v", a.pc())
	}
	a.setSecret("plaid_secret", "")
	a.reloadPlaid()
	if a.pc() != nil {
		t.Error("removing the secret should disable Plaid")
	}
}

func TestHelpers(t *testing.T) {
	for in, want := range map[string]string{"/charts": "/charts", "//evil.com": "/", "https://evil.com": "/", "/a\\b": "/", "": "/"} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q)=%q want %q", in, got, want)
		}
	}
	if !hostnameRe.MatchString("money.example.com") || hostnameRe.MatchString("localhost") || hostnameRe.MatchString("1.2.3.4") {
		t.Error("hostname validation")
	}
}

func TestSelfSignedCertIsStable(t *testing.T) {
	a := testApp(t)
	c1, err := a.localCert()
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := a.localCert()
	if string(c1.Certificate[0]) != string(c2.Certificate[0]) {
		t.Error("the local certificate must persist so the browser trust exception survives restarts")
	}
}

func TestListenersReconfigureAtRuntime(t *testing.T) {
	a := testApp(t)
	a.net = newNetManager(a, a.routes(), "127.0.0.1:0")
	a.net.Start()
	if st := a.net.Status(); len(st) != 1 || !st[0].OK {
		t.Fatalf("local listener: %+v", st)
	}
	// custom mode needs a password and a valid pair; reuse the stored self-signed pair as a stand-in
	a.localCert()
	a.setSecret("net_cert", a.getSetting("selfsigned_cert"))
	a.setSecret("net_key", a.getSecret("selfsigned_key"))
	a.setSetting("net_mode", "custom")
	a.setSetting("net_addr", "127.0.0.1:0")

	a.net.Reapply() // no password yet: refused, local listener untouched
	if st := a.net.Status(); len(st) != 2 || st[1].OK || !strings.Contains(st[1].Detail, "password") {
		t.Fatalf("expected a refusal without a password: %+v", st)
	}
	a.setSetting("password_hash", "x")
	a.net.Reapply()
	if st := a.net.Status(); len(st) != 2 || !st[1].OK {
		t.Fatalf("custom listener should be up: %+v", st)
	}
	a.setSetting("net_mode", "local")
	a.net.Reapply()
	if st := a.net.Status(); len(st) != 1 || !st[0].OK {
		t.Fatalf("back to local only: %+v", st)
	}
	// a busy port is reported, not fatal
	a.setSetting("net_mode", "custom")
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	a.setSetting("net_addr", ln.Addr().String())
	a.net.Reapply()
	if st := a.net.Status(); len(st) != 2 || st[1].OK || st[1].Detail == "" {
		t.Fatalf("busy port should be reported: %+v", st)
	}
	a.setSetting("net_mode", "local")
	a.net.Reapply()
}

func multipartUpload(t *testing.T, c *testClient, path, field, filename string, content []byte, extra map[string]string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile(field, filename)
	fw.Write(content)
	for k, v := range extra {
		mw.WriteField(k, v)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", c.srv.URL+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", c.srv.URL)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestPDFImportEndToEnd(t *testing.T) {
	a := testApp(t)
	c := newClient(t, a)
	pdfBytes, err := os.ReadFile(filepath.Join("testdata", "pdf", "bank_a.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	resp := multipartUpload(t, c, "/import/upload", "file", "statement.pdf", pdfBytes, nil)
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if resp.StatusCode != 200 || !strings.Contains(html, "Read 46 transactions") || !strings.Contains(html, "Table found by its header row") {
		t.Fatalf("mapping page: %d\n%.600s", resp.StatusCode, html)
	}
	tok := regexp.MustCompile(`name="token" value="([a-f0-9]{32})"`).FindStringSubmatch(html)
	if tok == nil {
		t.Fatal("no token on mapping page")
	}
	// the guessed mapping must be a valid debit/credit mapping on the ISO date layout
	if !strings.Contains(html, "2025-10-01") && !strings.Contains(html, "SALARY ACME") {
		t.Errorf("preview rows missing")
	}
	form := url.Values{"token": {tok[1]}, "date": {"0"}, "layout": {"0"}, "desc": {"1"}, "desc2": {"-1"}, "category": {"-1"},
		"mode": {"split"}, "amount": {"-1"}, "debit": {"2"}, "credit": {"3"}, "account": {"new"}, "new_name": {"Test Account"}, "action": {"import"}}
	r2 := c.post("/import/run", form)
	if r2.StatusCode != 303 || !strings.Contains(r2.Header.Get("Location"), "Imported+46") {
		t.Fatalf("import: %d %s", r2.StatusCode, r2.Header.Get("Location"))
	}
	var n int
	var out, in float64
	a.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN amount>0 THEN amount END),0), COALESCE(-SUM(CASE WHEN amount<0 THEN amount END),0) FROM transactions`).Scan(&n, &out, &in)
	_, exp := loadFixture(t, "bank_a")
	var wantOut, wantIn float64
	for _, e := range exp {
		wantOut += e.Debit
		wantIn += e.Credit
	}
	if n != 46 || math.Abs(out-wantOut) > 0.01 || math.Abs(in-wantIn) > 0.01 {
		t.Errorf("stored %d txns out %.2f in %.2f; want 46, %.2f, %.2f", n, out, in, wantOut, wantIn)
	}
	// SALARY ACME credits are recognised as income; merchants are categorised by the dictionary
	var salary, groceries int
	a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE category='Salary & Wages'`).Scan(&salary)
	a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE category='Groceries'`).Scan(&groceries)
	if salary == 0 || groceries == 0 {
		t.Errorf("categorisation after PDF import: salary=%d groceries=%d", salary, groceries)
	}
	// importing the same statement again adds nothing
	resp = multipartUpload(t, c, "/import/upload", "file", "statement.pdf", pdfBytes, nil)
	body, _ = io.ReadAll(resp.Body)
	tok = regexp.MustCompile(`name="token" value="([a-f0-9]{32})"`).FindStringSubmatch(string(body))
	var acct string
	a.db.QueryRow(`SELECT plaid_account_id FROM accounts LIMIT 1`).Scan(&acct)
	form.Set("token", tok[1])
	form.Set("account", acct)
	if r3 := c.post("/import/run", form); !strings.Contains(r3.Header.Get("Location"), "Imported+0") {
		t.Errorf("re-import should add nothing: %s", r3.Header.Get("Location"))
	}
}

func TestPDFUploadErrorsAreFriendly(t *testing.T) {
	a := testApp(t)
	c := newClient(t, a)
	scan, _ := os.ReadFile(filepath.Join("testdata", "pdf", "scan_i.pdf"))
	resp := multipartUpload(t, c, "/import/upload", "file", "scan.pdf", scan, nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 400 || !strings.Contains(string(body), "scanned image") {
		t.Errorf("scan: %d %.300s", resp.StatusCode, body)
	}
	resp = multipartUpload(t, c, "/import/upload", "file", "x.pdf", []byte("%PDF-1.4 junk"), nil)
	body, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != 400 || !strings.Contains(string(body), "couldn&#39;t read this PDF") && !strings.Contains(string(body), "couldn't read this PDF") {
		t.Errorf("junk: %d %.300s", resp.StatusCode, body)
	}
}
