package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Telegram bot: lets you categorise transactions from your phone.
//
// Once a day (at the hour chosen in Settings) the bot picks one uncategorised merchant, looks it up
// (your own history and rules, plus a Wikipedia search of the cleaned merchant name) and messages
// the configured recipient a short list of suggested categories. A reply of a number, a category
// name or "skip" categorises it (and other uncategorised transactions from the same merchant).
//
// It uses long polling, so it works from behind a home router with no open ports and no webhook.
// Only the configured chat is ever answered; every other chat is ignored (apart from /start,
// which tells a stranger their own chat ID, so you can find yours).
//
// Privacy: Telegram receives the transaction description and amount in the message. The online
// lookup still sends only the cleaned merchant name, never amounts or dates.

const (
	tgAPI          = "https://api.telegram.org"
	tgAskCooldown  = 30 * 24 * time.Hour // don't ask about the same merchant again for a month
	tgDefaultHour  = 9
	tgMaxOptions   = 5
	tgPendingKey   = "telegram_pending"
	tgNextData     = "next"              // callback data of the "Next transaction" button
	tgDonePrefix   = "Done:"             // start of the confirmation sent after categorising
	tgLastAskedKey = "telegram_last_ask" // date (YYYY-MM-DD) of the last daily message
)

var tgTokenRe = regexp.MustCompile(`^\d{5,}:[A-Za-z0-9_-]{20,}$`)

type telegramBot struct {
	a      *app
	base   string // overridden in tests
	client *http.Client

	mu      sync.Mutex // guards pending question handling and the fields below
	cancel  context.CancelFunc
	running bool
	lastErr string
	asking  atomic.Bool
}

func newTelegramBot(a *app) *telegramBot {
	return &telegramBot{a: a, base: tgAPI, client: &http.Client{}}
}

// ---- configuration -----------------------------------------------------------

func (b *telegramBot) token() string { return b.a.getSecret("telegram_token") }

// chatID is the one recipient (a user, or a group, which has a negative ID). 0 = not set.
func (b *telegramBot) chatID() int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(b.a.getSetting("telegram_chat_id")), 10, 64)
	return n
}

func (b *telegramBot) enabled() bool { return b.a.getSetting("telegram_enabled") == "1" }

func (b *telegramBot) hour() int {
	h, err := strconv.Atoi(b.a.getSetting("telegram_hour"))
	if err != nil || h < 0 || h > 23 {
		return tgDefaultHour
	}
	return h
}

func (b *telegramBot) configured() bool { return b.token() != "" && b.chatID() != 0 }

// Reload starts, restarts or stops the background loops to match the saved settings.
func (b *telegramBot) Reload() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	b.running = false
	if !b.enabled() || !b.configured() {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel, b.running = cancel, true
	go b.pollLoop(ctx)
	go b.dailyLoop(ctx)
}

func (b *telegramBot) setErr(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err == nil {
		b.lastErr = ""
		return
	}
	if msg := err.Error(); msg != b.lastErr {
		b.lastErr = msg
		log.Println("telegram:", msg)
	}
}

type tgStatus struct {
	Running bool
	Error   string
}

func (b *telegramBot) status() tgStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	return tgStatus{b.running, b.lastErr}
}

// ---- Bot API -----------------------------------------------------------------

type tgMessage struct {
	Text string `json:"text"`
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
}

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
	Callback *struct {
		ID      string     `json:"id"`
		Data    string     `json:"data"`
		Message *tgMessage `json:"message"`
	} `json:"callback_query"`
}

// call invokes a Bot API method. The token is part of the request URL, so transport errors are
// stripped of the URL before they can reach a log or the settings page.
func (b *telegramBot) call(ctx context.Context, method string, in, out any) error {
	tok := b.token()
	if tok == "" {
		return errors.New("no bot token saved")
	}
	timeout := 15 * time.Second
	if method == "getUpdates" {
		timeout = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", b.base+"/bot"+tok+"/"+method, bytes.NewReader(body))
	if err != nil {
		return errors.New("bad request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()
	var env struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&env); err != nil {
		return fmt.Errorf("%s: unexpected reply (HTTP %d)", method, resp.StatusCode)
	}
	if !env.OK {
		return fmt.Errorf("%s: %s", method, env.Description)
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

type tgButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

func (b *telegramBot) send(ctx context.Context, text string, keyboard [][]tgButton) error {
	msg := map[string]any{"chat_id": b.chatID(), "text": text, "disable_web_page_preview": true}
	if len(keyboard) > 0 {
		msg["reply_markup"] = map[string]any{"inline_keyboard": keyboard}
	}
	return b.call(ctx, "sendMessage", msg, nil)
}

// verify checks the token and sends a hello, so a typo in either shows up when saving.
func (b *telegramBot) verify(ctx context.Context) error {
	var me struct {
		Username string `json:"username"`
	}
	if err := b.call(ctx, "getMe", struct{}{}, &me); err != nil {
		return err
	}
	return b.send(ctx, "BrokerBuddy is connected. Once a day I'll ask you about one uncategorised transaction. Send /help to see what else I can do.", nil)
}

// ---- background loops ----------------------------------------------------------

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (b *telegramBot) pollLoop(ctx context.Context) {
	var offset int64
	for ctx.Err() == nil {
		var ups []tgUpdate
		err := b.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 30, "allowed_updates": []string{"message", "callback_query"}}, &ups)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.setErr(err)
			sleepCtx(ctx, 15*time.Second)
			continue
		}
		b.setErr(nil)
		for _, u := range ups {
			offset = u.UpdateID + 1
			b.dispatch(ctx, u)
		}
	}
}

func (b *telegramBot) dailyLoop(ctx context.Context) {
	for {
		b.maybeDaily(ctx, time.Now())
		sleepCtx(ctx, time.Minute)
		if ctx.Err() != nil {
			return
		}
	}
}

// maybeDaily sends today's question once the configured hour has passed.
func (b *telegramBot) maybeDaily(ctx context.Context, now time.Time) {
	today := now.Format("2006-01-02")
	if now.Hour() < b.hour() || b.a.getSetting(tgLastAskedKey) == today {
		return
	}
	if err := b.ask(ctx); err != nil && !errors.Is(err, errNothingToAsk) {
		b.setErr(err)
		return // transient (network, Telegram): try again next minute
	}
	b.a.setSetting(tgLastAskedKey, today)
}

func (b *telegramBot) dispatch(ctx context.Context, u tgUpdate) {
	chat := b.chatID()
	switch {
	case u.Message != nil:
		text := strings.TrimSpace(u.Message.Text)
		if u.Message.Chat.ID != chat {
			if cmd := commandOf(text); cmd == "/start" || cmd == "/id" {
				b.reply(ctx, u.Message.Chat.ID, fmt.Sprintf("Your chat ID is %d. Enter it under Settings → Telegram in BrokerBuddy to receive messages here.", u.Message.Chat.ID))
			}
			return // anyone else is ignored
		}
		if commandOf(text) == "/next" {
			go b.askAndReport(ctx)
			return
		}
		if isMoreText(text) {
			b.sendMore(ctx, chat, 0)
			return
		}
		if r := b.handle(text); r != "" {
			b.replyResult(ctx, chat, r)
		}
	case u.Callback != nil:
		cb := u.Callback
		if cb.Message == nil || cb.Message.Chat.ID != chat {
			return
		}
		b.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID}, nil)
		if cb.Data == tgNextData {
			go b.askAndReport(ctx)
			return
		}
		if seq, ok := strings.CutPrefix(cb.Data, "more:"); ok {
			n, _ := strconv.ParseInt(seq, 10, 64)
			b.sendMore(ctx, chat, n)
			return
		}
		if r := b.handleCallback(cb.Data); r != "" {
			b.replyResult(ctx, chat, r)
		}
	}
}

// replyResult sends a reply; when it confirms a categorisation it adds a button for the next one.
func (b *telegramBot) replyResult(ctx context.Context, chat int64, text string) {
	if !strings.HasPrefix(text, tgDonePrefix) {
		b.reply(ctx, chat, text)
		return
	}
	kb := [][]tgButton{{{"Next transaction", tgNextData}}}
	if err := b.send(ctx, text, kb); err != nil && ctx.Err() == nil {
		b.setErr(err)
	}
}

func (b *telegramBot) reply(ctx context.Context, chat int64, text string) {
	err := b.call(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": text}, nil)
	if err != nil && ctx.Err() == nil {
		b.setErr(err)
	}
}

func commandOf(text string) string {
	if !strings.HasPrefix(text, "/") {
		return ""
	}
	c, _, _ := strings.Cut(text, " ")
	c, _, _ = strings.Cut(c, "@") // "/next@MyBot" in group chats
	return strings.ToLower(c)
}

// ---- choosing what to ask ------------------------------------------------------

var errNothingToAsk = errors.New("no uncategorised transactions to ask about")

type tgCandidate struct {
	Key    string  // cleaned merchant name
	TxnID  string  // the largest transaction in the group, the one shown
	Name   string  // its description
	Amount float64 // its amount
	Date   string
	Count  int     // uncategorised transactions from this merchant
	Total  float64 // their combined value
	Why    string
}

// pickCandidate groups the uncategorised transactions by merchant and returns the one that matters
// most: either the biggest in total value or the most frequent, whichever stands out more against
// the other merchants. Merchants asked about recently are skipped.
func (a *app) pickCandidate(now time.Time) (*tgCandidate, error) {
	rows, err := a.db.Query(`SELECT plaid_txn_id, date, name, amount FROM transactions
		WHERE category='Uncategorized' AND manual=0 AND ignored=0 AND amount<>0`)
	if err != nil {
		return nil, err
	}
	groups := map[string]*tgCandidate{}
	biggest := map[string]float64{}
	for rows.Next() {
		var id, date, name string
		var amt float64
		rows.Scan(&id, &date, &name, &amt)
		key := merchantKey(name)
		if key == "" {
			key = norm(cleanText(name))
		}
		if key == "" {
			continue
		}
		g := groups[key]
		if g == nil {
			g = &tgCandidate{Key: key}
			groups[key] = g
		}
		abs := amt
		if abs < 0 {
			abs = -abs
		}
		g.Count++
		g.Total += abs
		if abs > biggest[key] {
			biggest[key] = abs
			g.TxnID, g.Name, g.Amount, g.Date = id, name, amt, date
		}
	}
	rows.Close()

	cut := now.Add(-tgAskCooldown).UTC().Format(time.RFC3339)
	var maxTotal, maxCount float64
	for k, g := range groups {
		var n int
		a.db.QueryRow(`SELECT COUNT(*) FROM telegram_asked WHERE key=? AND asked_at>?`, k, cut).Scan(&n)
		if n > 0 {
			delete(groups, k)
			continue
		}
		maxTotal = max(maxTotal, g.Total)
		maxCount = max(maxCount, float64(g.Count))
	}
	var best *tgCandidate
	var bestScore float64
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic on ties
	for _, k := range keys {
		g := groups[k]
		byValue, byCount := g.Total/maxTotal, float64(g.Count)/maxCount
		score := max(byValue, byCount)
		if best == nil || score > bestScore || (score == bestScore && g.Total > best.Total) {
			best, bestScore = g, score
			if byCount < byValue || g.Count == 1 {
				g.Why = "one of your highest-value uncategorised merchants"
			} else {
				g.Why = "one of your most frequent uncategorised merchants"
			}
		}
	}
	if best == nil {
		return nil, errNothingToAsk
	}
	return best, nil
}

// ---- the question --------------------------------------------------------------

type tgPending struct {
	Seq     int64    `json:"seq"`
	TxnID   string   `json:"txn"`
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Options []string `json:"options"`
}

func (b *telegramBot) pending() (p tgPending, ok bool) {
	s := b.a.getSetting(tgPendingKey)
	return p, s != "" && json.Unmarshal([]byte(s), &p) == nil && p.TxnID != ""
}

func (b *telegramBot) setPending(p *tgPending) {
	if p == nil {
		b.a.setSetting(tgPendingKey, "")
		return
	}
	js, _ := json.Marshal(p)
	b.a.setSetting(tgPendingKey, string(js))
}

func money(f float64) string {
	if f < 0 {
		return "-$" + fmtMoney(-f)
	}
	return "$" + fmtMoney(f)
}

// ask picks a transaction, looks up suggestions and sends the question.
func (b *telegramBot) ask(ctx context.Context) error {
	if !b.asking.CompareAndSwap(false, true) {
		return nil // one already in progress
	}
	defer b.asking.Store(false)

	cand, err := b.a.pickCandidate(time.Now())
	if err != nil {
		return err
	}
	var merchant string
	b.a.db.QueryRow(`SELECT merchant FROM transactions WHERE plaid_txn_id=?`, cand.TxnID).Scan(&merchant)
	opts, err := b.a.localSuggestions(cand.Name, merchant, cand.Amount)
	if err != nil {
		return err
	}
	lookedUp := false
	if web, werr := b.a.webSuggestCached(cand.Key); werr != nil {
		log.Println("telegram lookup:", werr) // fall back to local suggestions only
	} else if len(web) > 0 {
		opts, lookedUp = mergeSuggestions(opts, web), true
	}
	if len(opts) > tgMaxOptions {
		opts = opts[:tgMaxOptions]
	}

	p := &tgPending{Seq: time.Now().UnixNano(), TxnID: cand.TxnID, Key: cand.Key, Name: cand.Name}
	var sb strings.Builder
	sb.WriteString("Which category is this?\n\n")
	fmt.Fprintf(&sb, "%s\n%s on %s\n", cand.Name, money(cand.Amount), cand.Date)
	if cand.Count > 1 {
		fmt.Fprintf(&sb, "%d uncategorised transactions from this merchant, %s in total\n", cand.Count, money(cand.Total))
	}
	fmt.Fprintf(&sb, "(%s)\n\nSuggestions:\n", cand.Why)
	var kb [][]tgButton
	for i, o := range opts {
		p.Options = append(p.Options, o.Category)
		fmt.Fprintf(&sb, "%d. %s (%d%%) - %s\n", i+1, o.Category, o.Score, o.Why)
		kb = append(kb, []tgButton{{fmt.Sprintf("%d. %s", i+1, o.Category), fmt.Sprintf("pick:%d:%d", p.Seq, i+1)}})
	}
	if lookedUp {
		sb.WriteString("\nIncludes an online lookup of the merchant name.")
	}
	sb.WriteString("\nReply with a number, a category name, \"more\" for every category, or \"skip\".")
	kb = append(kb, []tgButton{{"More categories", fmt.Sprintf("more:%d", p.Seq)}, {"Skip", fmt.Sprintf("skip:%d", p.Seq)}})

	if err := b.send(ctx, sb.String(), kb); err != nil {
		return err
	}
	b.mu.Lock()
	b.setPending(p)
	b.mu.Unlock()
	b.a.db.Exec(`INSERT INTO telegram_asked(key,asked_at,outcome) VALUES(?,?,'') ON CONFLICT(key) DO UPDATE SET asked_at=excluded.asked_at, outcome=''`,
		cand.Key, time.Now().UTC().Format(time.RFC3339))
	return nil
}

func (b *telegramBot) askAndReport(ctx context.Context) {
	switch err := b.ask(ctx); {
	case errors.Is(err, errNothingToAsk):
		b.reply(ctx, b.chatID(), "Nothing to ask: every transaction has a category (or I asked about the rest recently).")
	case err != nil:
		b.setErr(err)
		b.reply(ctx, b.chatID(), "Sorry, that didn't work: "+err.Error())
	}
}

// ---- replies -------------------------------------------------------------------

const tgHelp = `I ask about one uncategorised transaction a day. Reply with a number, a category name or "skip".

/next - ask about another one now
/more - list every category
/skip - skip the current question
/status - how many transactions still need a category
/help - this message`

// handle answers a text message from the configured chat. An empty result sends nothing.
func (b *telegramBot) handle(text string) string {
	text = strings.TrimSpace(text)
	switch commandOf(text) {
	case "/start", "/help":
		return tgHelp
	case "/status":
		var n int
		var sum float64
		b.a.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(ABS(amount)),0) FROM transactions WHERE category='Uncategorized' AND manual=0 AND ignored=0`).Scan(&n, &sum)
		if n == 0 {
			return "Everything is categorised."
		}
		return fmt.Sprintf("%d transactions (%s) still have no category.", n, money(sum))
	case "/skip":
		return b.resolve("skip", 0)
	case "/more":
		return b.moreText()
	case "/next":
		return "" // handled by the caller, which needs the network
	case "":
		if isMoreText(text) {
			return b.moreText()
		}
	default:
		return "I don't know that command.\n\n" + tgHelp
	}
	return b.resolve(text, 0)
}

// handleCallback handles a tapped button: "pick:<seq>:<n>" or "skip:<seq>".
func (b *telegramBot) handleCallback(data string) string {
	parts := strings.Split(data, ":")
	if len(parts) < 2 {
		return ""
	}
	seq, _ := strconv.ParseInt(parts[1], 10, 64)
	switch parts[0] {
	case "pick":
		if len(parts) == 3 {
			n, _ := strconv.Atoi(parts[2])
			return b.resolve("", seq, n)
		}
	case "skip":
		return b.resolve("skip", seq)
	case "cat":
		if len(parts) == 3 {
			n, _ := strconv.Atoi(parts[2])
			if cats := pickableCategories(); n >= 1 && n <= len(cats) {
				return b.resolve(cats[n-1], seq)
			}
		}
	}
	return ""
}

// resolve applies an answer to the pending question. seq > 0 means it came from a button and must
// match the current question; an older message's buttons are refused.
func (b *telegramBot) resolve(text string, seq int64, pick ...int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, ok := b.pending()
	if !ok {
		return "There's no open question. Send /next and I'll pick one."
	}
	if seq != 0 && seq != p.Seq {
		return "That question has been replaced. Please answer the latest one."
	}
	t := strings.TrimSpace(text)
	switch strings.ToLower(t) {
	case "skip", "s", "no", "later":
		b.setPending(nil)
		b.a.db.Exec(`UPDATE telegram_asked SET outcome='skipped' WHERE key=?`, p.Key)
		return "Skipped. I won't ask about " + p.Key + " for a while."
	}
	cat := ""
	if len(pick) > 0 {
		if pick[0] >= 1 && pick[0] <= len(p.Options) {
			cat = p.Options[pick[0]-1]
		}
	} else if n, err := strconv.Atoi(t); err == nil {
		if n < 1 || n > len(p.Options) {
			return fmt.Sprintf("Pick a number from 1 to %d, type a category name, or \"skip\".", len(p.Options))
		}
		cat = p.Options[n-1]
	} else {
		matches := matchCategories(t)
		switch len(matches) {
		case 0:
			return "I don't know a category called “" + t + "”. Reply with a number, a category name, or \"skip\"."
		case 1:
			cat = matches[0]
		default:
			return "Which one? " + strings.Join(matches, ", ")
		}
	}
	if cat == "" {
		return "That choice is no longer available."
	}
	return b.apply(p, cat)
}

// apply categorises the asked-about transaction and the other uncategorised ones from the merchant.
func (b *telegramBot) apply(p tgPending, cat string) string {
	var n int
	b.a.db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE plaid_txn_id=?`, p.TxnID).Scan(&n)
	if n == 0 {
		b.setPending(nil)
		return "That transaction no longer exists (it may have been deleted or re-imported)."
	}
	applied, _, err := b.a.categorizeTxn(p.TxnID, cat, false)
	if err != nil {
		return "Sorry, I couldn't save that: " + err.Error()
	}
	// Anything from the same merchant still without a category counts as confirmed too.
	if rows, err := b.a.db.Query(`SELECT plaid_txn_id, name FROM transactions WHERE category='Uncategorized' AND manual=0 AND ignored=0`); err == nil {
		var ids []string
		for rows.Next() {
			var id, name string
			rows.Scan(&id, &name)
			k := merchantKey(name)
			if k == "" {
				k = norm(cleanText(name))
			}
			if k == p.Key {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			if _, err := b.a.db.Exec(`UPDATE transactions SET category=?, manual=1, confidence=100, cat_source='manual',
				edited_seq=(SELECT COALESCE(MAX(edited_seq),0)+1 FROM transactions) WHERE plaid_txn_id=?`, cat, id); err == nil {
				applied++
			}
		}
	}
	b.setPending(nil)
	b.a.db.Exec(`UPDATE telegram_asked SET outcome=? WHERE key=?`, cat, p.Key)
	msg := fmt.Sprintf("%s %s is now %s.", tgDonePrefix, p.Name, cat)
	if applied > 0 {
		msg += fmt.Sprintf(" Also applied to %d other transaction(s) from the same or a similar merchant.", applied)
	}
	return msg
}

// matchCategories finds categories by name: an exact (case-insensitive) match wins; otherwise any
// category containing the text.
func matchCategories(text string) []string {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return nil
	}
	var contains []string
	for _, c := range allCategories() {
		if c == "Uncategorized" {
			continue
		}
		lc := strings.ToLower(c)
		if lc == t {
			return []string{c}
		}
		if strings.Contains(lc, t) {
			contains = append(contains, c)
		}
	}
	return contains
}

// ---- settings handlers ---------------------------------------------------------

func (a *app) saveTelegram(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) { settingsRedirect(w, r, "telegram", msg, true) }
	if r.FormValue("remove") == "on" {
		for _, k := range []string{"telegram_token", "telegram_chat_id", "telegram_enabled", "telegram_hour", tgPendingKey, tgLastAskedKey} {
			a.setSetting(k, "")
		}
		a.tg.Reload()
		settingsRedirect(w, r, "telegram", "Telegram bot removed.", false)
		return
	}
	tok := strings.TrimSpace(r.FormValue("token"))
	if tok != "" && !tgTokenRe.MatchString(tok) {
		fail("That doesn't look like a bot token. It looks like 123456789:ABC… and comes from @BotFather.")
		return
	}
	chat := strings.TrimSpace(r.FormValue("chat_id"))
	if chat != "" {
		if n, err := strconv.ParseInt(chat, 10, 64); err != nil || n == 0 {
			fail("The recipient is a number (your chat ID). Message the bot /start and it will tell you yours.")
			return
		}
	}
	hour, err := strconv.Atoi(r.FormValue("hour"))
	if err != nil || hour < 0 || hour > 23 {
		fail("Choose an hour between 0 and 23.")
		return
	}
	enabled := r.FormValue("enabled") == "on"
	if enabled && tok == "" && a.getSetting("telegram_token") == "" {
		fail("Paste your bot token first.")
		return
	}
	if enabled && chat == "" {
		fail("Enter the recipient's chat ID.")
		return
	}
	if tok != "" {
		if err := a.setSecret("telegram_token", tok); err != nil {
			fail(err.Error())
			return
		}
	}
	a.setSetting("telegram_chat_id", chat)
	a.setSetting("telegram_hour", strconv.Itoa(hour))
	a.setSetting("telegram_enabled", map[bool]string{true: "1"}[enabled])
	if tok != "" { // a new bot or recipient: forget any open question
		a.tg.setPending(nil)
	}
	a.tg.Reload()
	if !enabled {
		settingsRedirect(w, r, "telegram", "Saved. The bot is switched off.", false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := a.tg.verify(ctx); err != nil {
		settingsRedirect(w, r, "telegram", "Saved, but Telegram didn't accept it: "+err.Error()+". Check the token, and that the recipient has pressed Start in a chat with the bot.", true)
		return
	}
	settingsRedirect(w, r, "telegram", fmt.Sprintf("Telegram bot connected. A test message was sent, and I'll ask about one transaction a day from %02d:00.", hour), false)
}

func (a *app) telegramAskNow(w http.ResponseWriter, r *http.Request) {
	if !a.tg.enabled() || !a.tg.configured() {
		settingsRedirect(w, r, "telegram", "Turn the Telegram bot on first.", true)
		return
	}
	// The lookup can take a while, so it runs in the background and arrives as a Telegram message.
	go a.tg.askAndReport(context.Background())
	settingsRedirect(w, r, "telegram", "Looking one up. You'll get a Telegram message in a few seconds.", false)
}

// hoursOfDay is 0..23, for the "send daily at" menu.
func hoursOfDay() []int {
	h := make([]int, 24)
	for i := range h {
		h[i] = i
	}
	return h
}

// ---- the full category list ------------------------------------------------------

func isMoreText(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "more", "/more", "all", "list", "categories":
		return true
	}
	return false
}

// pickableCategories is every category a transaction can be given, in a fixed order that the
// "cat:" buttons index into.
func pickableCategories() []string {
	var out []string
	for _, c := range allCategories() {
		if c != "Uncategorized" {
			out = append(out, c)
		}
	}
	return out
}

func (b *telegramBot) moreText() string {
	b.mu.Lock()
	_, ok := b.pending()
	b.mu.Unlock()
	if !ok {
		return "There's no open question. Send /next and I'll pick one."
	}
	return "All categories (reply with a name or part of one):\n\n" + strings.Join(pickableCategories(), "\n")
}

// sendMore sends every category as buttons for the open question. seq > 0 (from a button) must
// match the current question.
func (b *telegramBot) sendMore(ctx context.Context, chat int64, seq int64) {
	b.mu.Lock()
	p, ok := b.pending()
	b.mu.Unlock()
	switch {
	case !ok:
		b.reply(ctx, chat, "There's no open question. Send /next and I'll pick one.")
		return
	case seq != 0 && seq != p.Seq:
		b.reply(ctx, chat, "That question has been replaced. Please answer the latest one.")
		return
	}
	var kb [][]tgButton
	var row []tgButton
	for i, c := range pickableCategories() {
		row = append(row, tgButton{c, fmt.Sprintf("cat:%d:%d", p.Seq, i+1)})
		if len(row) == 2 {
			kb, row = append(kb, row), nil
		}
	}
	if len(row) > 0 {
		kb = append(kb, row)
	}
	kb = append(kb, []tgButton{{"Skip", fmt.Sprintf("skip:%d", p.Seq)}})
	if err := b.send(ctx, "Tap a category for “"+p.Name+"”, or type its name.", kb); err != nil {
		b.setErr(err)
	}
}
