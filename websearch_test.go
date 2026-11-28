package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestWebSearchQueryIsJustTheMerchant(t *testing.T) {
	q := webSearchQuery(llmTxn{Name: "SQ *BLUE DOOR CAFE 4412 NEWTOWN", Amount: 18.5, Date: "2026-03-01"})
	if q == "" || strings.ContainsAny(q, "0123456789$*") {
		t.Fatalf("query should be the cleaned name only: %q", q)
	}
	if !strings.HasSuffix(q, " Australia") {
		t.Errorf("query should name the country: %q", q)
	}
	if got := webSearchQuery(llmTxn{Name: "1234 5678"}); got != "" {
		t.Errorf("nothing searchable should give no query, got %q", got)
	}
}

func TestStripTagsAndContext(t *testing.T) {
	if got := stripTags("<strong>Blue</strong> Door &amp; Co"); got != "Blue Door & Co" {
		t.Errorf("got %q", got)
	}
	ctx := webContext("blue door Australia", []webResult{{"Blue Door Cafe", "https://www.bluedoor.com.au/menu", "Coffee and brunch"}})
	if !strings.Contains(ctx, "Blue Door Cafe (bluedoor.com.au): Coffee and brunch") {
		t.Errorf("got %q", ctx)
	}
}

func TestAskAIWithBraveSearch(t *testing.T) {
	a := testApp(t)
	f, llm := newFakeLLM(t, `{"category":"Dining & Takeaway","confidence":88,"reason":"A café."}`)
	a.setSetting(llmProviderKey, "anthropic")
	a.setSetting(llmBaseURLKey, llm.URL)
	a.setSecret(llmAPIKeyKey, "sk-ant-test")

	var gotQ url.Values
	var gotKey string
	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQ, gotKey = r.URL.Query(), r.Header.Get("X-Subscription-Token")
		w.Write([]byte(`{"web":{"results":[{"title":"<strong>Blue Door</strong> Cafe","url":"https://bluedoor.com.au","description":"Specialty coffee and brunch in Newtown"}]}}`))
	}))
	defer brave.Close()
	old := braveEndpoint
	braveEndpoint = brave.URL
	defer func() { braveEndpoint = old }()
	a.setSetting(llmSearchKey, "brave")
	a.setSecret(llmSearchKeyPrefix+"brave", "brave-key")

	ans, err := a.llmSuggest(context.Background(), llmTxn{Name: "BLUE DOOR 4412 NEWTOWN", Amount: 18.5, Date: "2026-03-01"})
	if err != nil || ans.Category != "Dining & Takeaway" {
		t.Fatalf("got %+v %v", ans, err)
	}
	if gotKey != "brave-key" || strings.Contains(gotQ.Get("q"), "18") || !strings.Contains(gotQ.Get("q"), "blue door") {
		t.Errorf("search should send the key and only the merchant name: key=%q q=%q", gotKey, gotQ.Get("q"))
	}
	msg, _ := json.Marshal(f.bodies[0]["messages"])
	if !strings.Contains(string(msg), "Specialty coffee and brunch") || !strings.Contains(string(msg), "Blue Door Cafe (bluedoor.com.au)") {
		t.Errorf("search results should be in the prompt: %s", msg)
	}
	if sys, _ := f.bodies[0]["system"].(string); !strings.Contains(sys, "Web search results") {
		t.Errorf("system prompt should explain the results: %q", sys)
	}
	if _, ok := f.bodies[0]["tools"]; ok {
		t.Error("a separate search shouldn't give the model a search tool")
	}
	if ans.Searched != "Brave Search (1 result)" {
		t.Errorf("searched = %q", ans.Searched)
	}
}

func TestAskAISearchFailureStillAnswers(t *testing.T) {
	a := testApp(t)
	_, llm := newFakeLLM(t, `{"category":"Groceries","confidence":60,"reason":"Probably food."}`)
	a.setSetting(llmProviderKey, "openai")
	a.setSetting(llmModelKey, "llama3.2")
	a.setSetting(llmBaseURLKey, llm.URL+"/v1")
	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403) // json format not enabled
	}))
	defer searx.Close()
	a.setSetting(llmSearchKey, "searxng")
	a.setSetting(llmSearchURLKey, searx.URL)

	ans, err := a.llmSuggest(context.Background(), llmTxn{Name: "MYSTERY MART", Amount: 40})
	if err != nil || ans.Category != "Groceries" {
		t.Fatalf("the AI should still answer: %+v %v", ans, err)
	}
	if ans.SearchErr == nil || !strings.Contains(ans.SearchErr.Error(), "SearXNG") || ans.Searched != "" {
		t.Errorf("search failure should be reported: %+v", ans)
	}
}

func TestAskAIWithAnthropicWebSearch(t *testing.T) {
	a := testApp(t)
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		mu.Lock()
		bodies = append(bodies, in)
		n := len(bodies)
		mu.Unlock()
		if n == 1 { // the search pauses the turn
			w.Write([]byte(`{"stop_reason":"pause_turn","content":[{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"mystery mart"}}]}`))
			return
		}
		w.Write([]byte(`{"stop_reason":"end_turn","content":[
			{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","title":"Mystery Mart","url":"https://example.com"}]},
			{"type":"text","text":"{\"category\":\"Groceries\",","citations":[{"type":"web_search_result_location","url":"https://example.com"}]},
			{"type":"text","text":"\"confidence\":80,\"reason\":\"An independent grocer.\"}"}]}`))
	}))
	defer srv.Close()
	a.setSetting(llmProviderKey, "anthropic")
	a.setSetting(llmBaseURLKey, srv.URL)
	a.setSecret(llmAPIKeyKey, "sk-ant-test")
	a.setSetting(llmSearchKey, "anthropic")

	ans, err := a.llmSuggest(context.Background(), llmTxn{Name: "MYSTERY MART", Amount: 40})
	if err != nil || ans.Category != "Groceries" || ans.Reason != "An independent grocer." {
		t.Fatalf("got %+v %v", ans, err)
	}
	if ans.Searched != "Anthropic web search (1 search)" {
		t.Errorf("searched = %q", ans.Searched)
	}
	if len(bodies) != 2 {
		t.Fatalf("pause_turn should be resumed once, got %d requests", len(bodies))
	}
	tools, _ := json.Marshal(bodies[0]["tools"])
	if !strings.Contains(string(tools), `"web_search_20260209"`) || !strings.Contains(string(tools), `"max_uses":3`) {
		t.Errorf("web search tool expected: %s", tools)
	}
	if oc, _ := bodies[0]["output_config"].(map[string]any); oc["format"] != nil {
		t.Errorf("no JSON schema with web search (citations): %v", oc)
	}
	msgs, _ := json.Marshal(bodies[1]["messages"])
	if !strings.Contains(string(msgs), `"role":"assistant"`) || !strings.Contains(string(msgs), "srvtoolu_1") {
		t.Errorf("resume should send the paused reply back: %s", msgs)
	}
	if strings.Contains(string(msgs), "Continue") {
		t.Error("resume must not add a user message")
	}
}

func TestAnthropicSearchToolVersion(t *testing.T) {
	if anthropicSearchTool("claude-opus-5-5")["type"] != "web_search_20260209" {
		t.Error("current models get the dynamic-filtering version")
	}
	if anthropicSearchTool("claude-haiku-4-5")["type"] != "web_search_20250305" {
		t.Error("older models get the basic version")
	}
}

func TestSaveLLMSearchValidation(t *testing.T) {
	a := testApp(t)
	post := func(form url.Values) string {
		r := httptest.NewRequest("POST", "/settings/ai", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.saveLLM(w, r)
		return w.Header().Get("Location")
	}
	loc := post(url.Values{"provider": {"openai"}, "model": {"llama3.2"}, "search": {"anthropic"}})
	if !strings.Contains(loc, "err=") || a.getSetting(llmSearchKey) != "" {
		t.Errorf("Anthropic search needs the Anthropic provider: %s", loc)
	}
	loc = post(url.Values{"provider": {"openai"}, "model": {"llama3.2"}, "search": {"brave"}})
	if !strings.Contains(loc, "err=") {
		t.Errorf("Brave needs a key: %s", loc)
	}
	loc = post(url.Values{"provider": {"openai"}, "model": {"llama3.2"}, "search": {"brave"}, "search_key": {"bk"}})
	if strings.Contains(loc, "err=") || a.getSetting(llmSearchKey) != "brave" || a.getSecret(llmSearchKeyPrefix+"brave") != "bk" {
		t.Errorf("save failed: %s", loc)
	}
	// Switching service doesn't reuse another service's key.
	if loc = post(url.Values{"provider": {"openai"}, "model": {"llama3.2"}, "search": {"tavily"}}); !strings.Contains(loc, "err=") {
		t.Errorf("Tavily needs its own key: %s", loc)
	}
	loc = post(url.Values{"provider": {"openai"}, "model": {"llama3.2"}, "search": {"searxng"}, "search_url": {"localhost:8888"}})
	if !strings.Contains(loc, "err=") {
		t.Errorf("SearXNG address needs a scheme: %s", loc)
	}
	post(url.Values{"remove": {"on"}})
	if a.getSetting(llmSearchKey) != "" || a.getSetting(llmSearchKeyPrefix+"brave") != "" {
		t.Error("remove should clear search settings")
	}
}
