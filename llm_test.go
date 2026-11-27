package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeLLM serves both the Anthropic and the OpenAI-compatible endpoint and records requests.
type fakeLLM struct {
	mu      sync.Mutex
	paths   []string
	headers []http.Header
	bodies  []map[string]any
	answer  string // the model's text reply
	status  int
}

func newFakeLLM(t *testing.T, answer string) (*fakeLLM, *httptest.Server) {
	f := &fakeLLM{answer: answer, status: 200}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.paths, f.headers, f.bodies = append(f.paths, r.URL.Path), append(f.headers, r.Header.Clone()), append(f.bodies, in)
		status, ans := f.status, f.answer
		f.mu.Unlock()
		w.WriteHeader(status)
		if status != 200 {
			w.Write([]byte(`{"error":{"message":"invalid x-api-key"}}`))
			return
		}
		js, _ := json.Marshal(ans)
		if strings.HasSuffix(r.URL.Path, "/v1/messages") {
			w.Write([]byte(`{"stop_reason":"end_turn","content":[{"type":"text","text":` + string(js) + `}]}`))
		} else {
			w.Write([]byte(`{"choices":[{"message":{"content":` + string(js) + `}}]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func TestParseLLMAnswer(t *testing.T) {
	cats := pickableCategories()
	a, err := parseLLMAnswer("Sure!\n```json\n{\"category\":\"groceries\",\"confidence\":140,\"reason\":\"A supermarket.\"}\n```", cats)
	if err != nil || a.Category != "Groceries" || a.Confidence != 100 || a.Reason != "A supermarket." {
		t.Fatalf("got %+v %v", a, err)
	}
	if _, err := parseLLMAnswer(`{"category":"Spaceships"}`, cats); err == nil || !strings.Contains(err.Error(), "Spaceships") {
		t.Errorf("unknown category should fail: %v", err)
	}
	if _, err := parseLLMAnswer("I think groceries", cats); err == nil {
		t.Error("non-JSON reply should fail")
	}
}

func TestLLMAnthropicRequest(t *testing.T) {
	a := testApp(t)
	f, srv := newFakeLLM(t, `{"category":"Groceries","confidence":90,"reason":"Supermarket chain."}`)
	a.setSetting(llmProviderKey, "anthropic")
	a.setSetting(llmBaseURLKey, srv.URL)
	a.setSecret(llmAPIKeyKey, "sk-ant-test")
	if !a.llmEnabled() {
		t.Fatal("should be enabled")
	}
	ans, err := a.llmSuggest(context.Background(), llmTxn{Name: "WOOLWORTHS 1234", Amount: 82.4, Date: "2026-03-01"})
	if err != nil || ans.Category != "Groceries" {
		t.Fatalf("got %+v %v", ans, err)
	}
	h, body := f.headers[0], f.bodies[0]
	if f.paths[0] != "/v1/messages" || h.Get("x-api-key") != "sk-ant-test" || h.Get("anthropic-version") == "" {
		t.Errorf("bad request: %s %v", f.paths[0], h)
	}
	if body["model"] != llmAnthropicModel || body["fallbacks"] != "default" || h.Get("anthropic-beta") == "" {
		t.Errorf("default model with fallbacks expected: %v", body)
	}
	msg, _ := json.Marshal(body["messages"])
	if !strings.Contains(string(msg), "WOOLWORTHS 1234") || !strings.Contains(string(msg), "$82.40, money out") {
		t.Errorf("prompt should describe the transaction: %s", msg)
	}

	f.status = 401
	if _, err := a.llmSuggest(context.Background(), llmTxn{Name: "X"}); err == nil || !strings.Contains(err.Error(), "invalid x-api-key") ||
		strings.Contains(err.Error(), "sk-ant-test") {
		t.Errorf("error should explain without leaking the key: %v", err)
	}
}

func TestLLMOpenAICompatibleWithoutKey(t *testing.T) {
	a := testApp(t)
	f, srv := newFakeLLM(t, `{"category":"Fuel","confidence":70,"reason":"Petrol station."}`)
	a.setSetting(llmProviderKey, "openai")
	a.setSetting(llmBaseURLKey, srv.URL+"/v1/")
	if a.llmEnabled() {
		t.Fatal("openai needs a model name")
	}
	a.setSetting(llmModelKey, "llama3.2")
	ans, err := a.llmSuggest(context.Background(), llmTxn{Name: "AMPOL", Amount: 60})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(pickableCategories(), ans.Category) {
		t.Fatalf("got %+v", ans)
	}
	if f.paths[0] != "/v1/chat/completions" || f.headers[0].Get("Authorization") != "" || f.bodies[0]["model"] != "llama3.2" {
		t.Errorf("bad request: %s %v %v", f.paths[0], f.headers[0], f.bodies[0])
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestBotAskAI(t *testing.T) {
	a, tg := tgTestApp(t)
	addTxn(t, a, "u1", "2026-03-01", "MYSTERY MART", 40, "Uncategorized", 0, false, "")

	// Without AI set up there is no button.
	a.tg.ask(context.Background())
	kb, _ := json.Marshal(tg.sent[0]["reply_markup"])
	if strings.Contains(string(kb), "Ask AI") {
		t.Fatal("no Ask AI button without a provider")
	}

	f, srv := newFakeLLM(t, `{"category":"Groceries","confidence":85,"reason":"Sounds like a supermarket."}`)
	a.setSetting(llmProviderKey, "anthropic")
	a.setSetting(llmBaseURLKey, srv.URL)
	a.setSecret(llmAPIKeyKey, "sk-ant-test")
	a.db.Exec(`DELETE FROM telegram_asked`)
	a.tg.ask(context.Background())
	p, _ := a.tg.pending()
	kb, _ = json.Marshal(tg.sent[len(tg.sent)-1]["reply_markup"])
	if !strings.Contains(string(kb), "Ask AI") || !strings.Contains(string(kb), "ai:") {
		t.Fatalf("Ask AI button expected: %s", kb)
	}

	a.tg.askAI(context.Background(), 555, p.Seq)
	last := tg.sent[len(tg.sent)-1]
	text, _ := last["text"].(string)
	if !strings.Contains(text, "Groceries") || !strings.Contains(text, "85%") || !strings.Contains(text, "supermarket") {
		t.Fatalf("unexpected AI reply: %q", text)
	}
	if msg, _ := json.Marshal(f.bodies[0]["messages"]); !strings.Contains(string(msg), "MYSTERY MART") {
		t.Errorf("AI should be asked about the pending transaction: %s", msg)
	}
	// Nothing changes until the user picks; then the "Use" button applies it.
	var cat string
	a.db.QueryRow(`SELECT category FROM transactions WHERE plaid_txn_id='u1'`).Scan(&cat)
	if cat != "Uncategorized" {
		t.Fatalf("AI must not categorise on its own, got %q", cat)
	}
	var mk struct {
		Rows [][]tgButton `json:"inline_keyboard"`
	}
	kb, _ = json.Marshal(last["reply_markup"])
	json.Unmarshal(kb, &mk)
	use := mk.Rows[0][0]
	if use.Text != "Use Groceries" {
		t.Fatalf("first button should use the suggestion: %+v", use)
	}
	if r := a.tg.handleCallback(use.Data); !strings.Contains(r, "Done") || !strings.Contains(r, "Groceries") {
		t.Errorf("use button: %q", r)
	}

	// Stale button and no open question.
	a.tg.askAI(context.Background(), 555, p.Seq)
	if txt := tg.texts(); !strings.Contains(txt[len(txt)-1], "no open question") {
		t.Errorf("got %q", txt[len(txt)-1])
	}
}
