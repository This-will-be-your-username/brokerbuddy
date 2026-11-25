package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testTgToken = "123456789:AAEhBP0av28ABCDEFGHIJKLMNOPQRSTUV"

// fakeTelegram records the Bot API calls the bot makes.
type fakeTelegram struct {
	mu   sync.Mutex
	sent []map[string]any
	fail string // when set, every call answers ok=false with this description
}

func (f *fakeTelegram) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.sent {
		if s, ok := m["text"].(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func tgTestApp(t *testing.T) (*app, *fakeTelegram) {
	t.Helper()
	a := testApp(t)
	withAccount(a)
	f := &fakeTelegram{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/bot"+testTgToken+"/") {
			http.NotFound(w, r)
			return
		}
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.fail != "" {
			w.Write([]byte(`{"ok":false,"description":"` + f.fail + `"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			f.sent = append(f.sent, in)
		}
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			f.mu.Unlock()
			time.Sleep(200 * time.Millisecond) // like a long poll that found nothing
			f.mu.Lock()
			w.Write([]byte(`{"ok":true,"result":[]}`))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"username":"bb_test_bot"}}`))
	}))
	t.Cleanup(srv.Close)
	a.tg.base = srv.URL
	oldWeb := webSearch
	webSearch = func(key string) ([]suggestion, error) {
		return []suggestion{{"Groceries", 58, "Wikipedia: " + key + " — supermarket chain", "web"}}, nil
	}
	t.Cleanup(func() { webSearch = oldWeb })
	a.setSecret("telegram_token", testTgToken)
	a.setSetting("telegram_chat_id", "555")
	a.setSetting("telegram_enabled", "1")
	return a, f
}

func TestPickCandidatePrefersHighValueOrFrequent(t *testing.T) {
	a := testApp(t)
	withAccount(a)
	addTxn(t, a, "big", "2026-03-01", "BIGSPEND PTY LTD", 2000, "Uncategorized", 0, false, "")
	for i, d := range []string{"01", "02", "03", "04", "05", "06"} {
		addTxn(t, a, "f"+string(rune('a'+i)), "2026-03-"+d, "FREQUENT CAFE", 12, "Uncategorized", 0, false, "")
	}
	addTxn(t, a, "ok", "2026-03-02", "CATEGORISED", 9000, "Groceries", 90, false, "builtin")
	addTxn(t, a, "man", "2026-03-02", "MANUAL ONE", 9000, "Uncategorized", 100, true, "manual")
	now := time.Now()

	c, err := a.pickCandidate(now)
	if err != nil {
		t.Fatal(err)
	}
	if c.TxnID != "big" || c.Amount != 2000 || !strings.Contains(c.Why, "highest-value") {
		t.Fatalf("want the big spend first, got %+v", c)
	}
	// once asked, it is skipped and the frequent merchant is next
	a.db.Exec(`INSERT INTO telegram_asked(key,asked_at) VALUES(?,?)`, c.Key, now.UTC().Format(time.RFC3339))
	c, err = a.pickCandidate(now)
	if err != nil || c.Count != 6 || !strings.Contains(c.Why, "frequent") {
		t.Fatalf("want the frequent merchant, got %+v %v", c, err)
	}
	// after the cooldown the first one comes back
	c, _ = a.pickCandidate(now.Add(tgAskCooldown + time.Hour))
	if c == nil || c.TxnID != "big" {
		t.Fatalf("cooldown should expire, got %+v", c)
	}
	a.db.Exec(`UPDATE transactions SET category='Groceries' WHERE category='Uncategorized'`)
	if _, err := a.pickCandidate(now); err != errNothingToAsk {
		t.Errorf("nothing to ask expected, got %v", err)
	}
}

func TestBotAsksAndCategorisesFromReply(t *testing.T) {
	a, f := tgTestApp(t)
	addTxn(t, a, "u1", "2026-03-01", "MYSTERY MART 11", 40, "Uncategorized", 0, false, "")
	addTxn(t, a, "u2", "2026-03-08", "MYSTERY MART 22", 25, "Uncategorized", 0, false, "")
	addTxn(t, a, "u3", "2026-03-09", "OTHER THING", 5, "Uncategorized", 0, false, "")

	if err := a.tg.ask(context.Background()); err != nil {
		t.Fatal(err)
	}
	txt := f.texts()
	if len(txt) != 1 || !strings.Contains(txt[0], "MYSTERY MART") || !strings.Contains(txt[0], "$40.00") ||
		!strings.Contains(txt[0], "Groceries") || !strings.Contains(txt[0], "2 uncategorised") {
		t.Fatalf("unexpected question: %q", txt)
	}
	if f.sent[0]["chat_id"].(float64) != 555 || f.sent[0]["reply_markup"] == nil {
		t.Errorf("should go to the recipient with buttons: %v", f.sent[0])
	}
	p, ok := a.tg.pending()
	if !ok || p.TxnID != "u1" || len(p.Options) == 0 {
		t.Fatalf("question not remembered: %+v", p)
	}
	idx := 0
	for i, o := range p.Options {
		if o == "Groceries" {
			idx = i + 1
		}
	}
	if idx == 0 {
		t.Fatalf("Groceries not offered: %v", p.Options)
	}

	reply := a.tg.handle(string(rune('0' + idx)))
	if !strings.Contains(reply, "Groceries") || !strings.Contains(reply, "Done") {
		t.Errorf("reply: %q", reply)
	}
	for _, id := range []string{"u1", "u2"} {
		var cat string
		var man int
		a.db.QueryRow(`SELECT category, manual FROM transactions WHERE plaid_txn_id=?`, id).Scan(&cat, &man)
		if cat != "Groceries" || (id == "u1" && man != 1) {
			t.Errorf("%s: want Groceries (u1 confirmed), got %q manual=%d", id, cat, man)
		}
	}
	var other string
	a.db.QueryRow(`SELECT category FROM transactions WHERE plaid_txn_id='u3'`).Scan(&other)
	if other != "Uncategorized" {
		t.Errorf("unrelated merchant must be untouched, got %q", other)
	}
	if _, ok := a.tg.pending(); ok {
		t.Error("question should be closed")
	}
	if got := a.tg.handle("1"); !strings.Contains(got, "no open question") {
		t.Errorf("a second reply has nothing to answer: %q", got)
	}
}

func TestBotReplyByNameSkipAndStaleButton(t *testing.T) {
	a, f := tgTestApp(t)
	addTxn(t, a, "u1", "2026-03-01", "MYSTERY MART", 40, "Uncategorized", 0, false, "")
	a.tg.ask(context.Background())
	p, _ := a.tg.pending()

	if r := a.tg.handle("banana"); !strings.Contains(r, "don't know a category") {
		t.Errorf("unknown text: %q", r)
	}
	if r := a.tg.handle("99"); !strings.Contains(r, "Pick a number") {
		t.Errorf("bad number: %q", r)
	}
	if r := a.tg.handle("dining"); !strings.Contains(r, "Dining & Takeaway") || !strings.Contains(r, "Done") {
		t.Errorf("category by partial name: %q", r)
	}
	var cat string
	a.db.QueryRow(`SELECT category FROM transactions WHERE plaid_txn_id='u1'`).Scan(&cat)
	if cat != "Dining & Takeaway" {
		t.Errorf("got %q", cat)
	}

	// skip
	addTxn(t, a, "u2", "2026-03-01", "SECOND PLACE", 30, "Uncategorized", 0, false, "")
	a.tg.ask(context.Background())
	if r := a.tg.handle("skip"); !strings.Contains(r, "Skipped") {
		t.Errorf("skip: %q", r)
	}
	var out string
	a.db.QueryRow(`SELECT outcome FROM telegram_asked WHERE key='second place'`).Scan(&out)
	if out != "skipped" {
		t.Errorf("outcome %q", out)
	}

	// a button from an older question is refused
	addTxn(t, a, "u3", "2026-03-01", "THIRD PLACE", 20, "Uncategorized", 0, false, "")
	a.tg.ask(context.Background())
	stale := "pick:" + itoa(p.Seq) + ":1"
	if r := a.tg.handleCallback(stale); !strings.Contains(r, "replaced") {
		t.Errorf("stale button: %q", r)
	}
	cur, _ := a.tg.pending()
	if r := a.tg.handleCallback("pick:" + itoa(cur.Seq) + ":1"); !strings.Contains(r, "Done") {
		t.Errorf("current button: %q", r)
	}
	_ = f
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestBotIgnoresOtherChats(t *testing.T) {
	a, f := tgTestApp(t)
	addTxn(t, a, "u1", "2026-03-01", "MYSTERY MART", 40, "Uncategorized", 0, false, "")
	a.tg.ask(context.Background())
	before := len(f.texts())

	stranger := &tgMessage{Text: "1"}
	stranger.Chat.ID = 999
	a.tg.dispatch(context.Background(), tgUpdate{Message: stranger})
	if len(f.texts()) != before {
		t.Error("a stranger must get no answer")
	}
	var cat string
	a.db.QueryRow(`SELECT category FROM transactions WHERE plaid_txn_id='u1'`).Scan(&cat)
	if cat != "Uncategorized" {
		t.Error("a stranger must not categorise anything")
	}
	stranger.Text = "/start"
	a.tg.dispatch(context.Background(), tgUpdate{Message: stranger})
	if txt := f.texts(); len(txt) != before+1 || !strings.Contains(txt[before], "999") {
		t.Errorf("/start should reveal only their own chat ID: %v", txt)
	}
	if f.sent[len(f.sent)-1]["chat_id"].(float64) != 999 {
		t.Error("reply goes to the asker")
	}
}

func TestDailySendsOncePerDayAfterConfiguredHour(t *testing.T) {
	a, f := tgTestApp(t)
	a.setSetting("telegram_hour", "9")
	addTxn(t, a, "u1", "2026-03-01", "MYSTERY MART", 40, "Uncategorized", 0, false, "")
	day := time.Date(2026, 4, 2, 0, 0, 0, 0, time.Local)

	a.tg.maybeDaily(context.Background(), day.Add(8*time.Hour))
	if len(f.texts()) != 0 {
		t.Fatal("too early")
	}
	a.tg.maybeDaily(context.Background(), day.Add(9*time.Hour))
	a.tg.maybeDaily(context.Background(), day.Add(15*time.Hour))
	if len(f.texts()) != 1 {
		t.Fatalf("want exactly one message today, got %d", len(f.texts()))
	}
	a.tg.maybeDaily(context.Background(), day.Add(33*time.Hour)) // next day: nothing new to ask (cooldown)
	if len(f.texts()) != 1 {
		t.Errorf("same merchant is not asked again within the cooldown")
	}
}

func TestSaveTelegramSettings(t *testing.T) {
	a, f := tgTestApp(t)
	a.setSetting("telegram_token", "")
	a.setSetting("telegram_chat_id", "")
	a.setSetting("telegram_enabled", "")
	c := newClient(t, a)
	post := func(v url.Values) string {
		r := c.post("/settings/telegram", v)
		loc, _ := r.Location()
		return loc.Query().Get("msg") + loc.Query().Get("err")
	}
	if m := post(url.Values{"token": {"nonsense"}, "chat_id": {"1"}, "hour": {"9"}}); !strings.Contains(m, "bot token") {
		t.Errorf("bad token: %q", m)
	}
	if m := post(url.Values{"token": {testTgToken}, "chat_id": {"abc"}, "hour": {"9"}}); !strings.Contains(m, "number") {
		t.Errorf("bad chat id: %q", m)
	}
	if m := post(url.Values{"token": {testTgToken}, "chat_id": {"555"}, "hour": {"30"}}); !strings.Contains(m, "hour") {
		t.Errorf("bad hour: %q", m)
	}
	if m := post(url.Values{"token": {testTgToken}, "chat_id": {""}, "hour": {"9"}, "enabled": {"on"}}); !strings.Contains(m, "chat ID") {
		t.Errorf("enabled without recipient: %q", m)
	}

	m := post(url.Values{"token": {testTgToken}, "chat_id": {"555"}, "hour": {"7"}, "enabled": {"on"}})
	if !strings.Contains(m, "connected") {
		t.Fatalf("save: %q", m)
	}
	if got := f.texts(); len(got) != 1 || !strings.Contains(got[0], "connected") {
		t.Errorf("test message not sent: %v", got)
	}
	if raw := a.getSetting("telegram_token"); raw == "" || strings.Contains(raw, "AAEhBP0") {
		t.Errorf("token must be stored encrypted, got %q", raw)
	}
	if a.getSecret("telegram_token") != testTgToken || a.tg.chatID() != 555 || a.tg.hour() != 7 || !a.tg.enabled() {
		t.Error("settings not saved")
	}
	if !a.tg.status().Running {
		t.Error("bot should be running")
	}
	// the settings page shows the recipient but never the token
	body := readBody(c.get("/settings"))
	if !strings.Contains(body, `value="555"`) || strings.Contains(body, "AAEhBP0") {
		t.Error("settings page should show the recipient and hide the token")
	}
	// blank token keeps the saved one; a rejected token is reported
	f.fail = "Unauthorized"
	if m := post(url.Values{"chat_id": {"555"}, "hour": {"7"}, "enabled": {"on"}}); !strings.Contains(m, "Unauthorized") {
		t.Errorf("Telegram errors should be shown: %q", m)
	}
	if a.getSecret("telegram_token") != testTgToken {
		t.Error("blank token must keep the saved one")
	}
	f.fail = ""
	post(url.Values{"remove": {"on"}})
	if a.tg.configured() || a.tg.status().Running {
		t.Error("remove should clear and stop the bot")
	}
}

func TestTelegramErrorsNeverLeakTheToken(t *testing.T) {
	a, _ := tgTestApp(t)
	a.tg.base = "http://127.0.0.1:1" // nothing listens here
	err := a.tg.call(context.Background(), "getMe", struct{}{}, nil)
	if err == nil || strings.Contains(err.Error(), "AAEhBP0") {
		t.Errorf("error must not contain the token: %v", err)
	}
}

func readBody(r *http.Response) string {
	defer r.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			return sb.String()
		}
	}
}

func TestBotMoreShowsEveryCategory(t *testing.T) {
	a, f := tgTestApp(t)
	addTxn(t, a, "u1", "2026-03-01", "MYSTERY MART", 40, "Uncategorized", 0, false, "")
	a.tg.ask(context.Background())
	if kb, _ := json.Marshal(f.sent[0]["reply_markup"]); !strings.Contains(string(kb), "more:") {
		t.Errorf("question should offer a More button: %s", kb)
	}
	if r := a.tg.handle("more"); !strings.Contains(r, "Pets") || !strings.Contains(r, "Gambling") || strings.Contains(r, "Uncategorized") {
		t.Errorf("text list: %q", r)
	}
	p, _ := a.tg.pending()
	a.tg.sendMore(context.Background(), 555, p.Seq)
	last := f.sent[len(f.sent)-1]
	kb, _ := json.Marshal(last["reply_markup"])
	if n := strings.Count(string(kb), "cat:"); n != len(pickableCategories()) {
		t.Errorf("want a button per category, got %d", n)
	}
	cats := pickableCategories()
	r := a.tg.handleCallback("cat:" + itoa(p.Seq) + ":" + strconv.Itoa(len(cats)))
	var got string
	a.db.QueryRow(`SELECT category FROM transactions WHERE plaid_txn_id='u1'`).Scan(&got)
	if got != cats[len(cats)-1] || !strings.Contains(r, "Done") {
		t.Errorf("tapped category not applied: %q %q", got, r)
	}
}
