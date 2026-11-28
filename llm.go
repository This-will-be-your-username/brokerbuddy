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
	"strings"
	"time"
)

// Optional AI (LLM) integration. When configured in Settings, the Telegram bot offers an
// "Ask AI" button that asks the model which category a transaction most likely belongs to.
// The answer is only a suggestion: you still pick the category.
//
// Two kinds of endpoint are supported:
//   - anthropic: the Claude Messages API (https://api.anthropic.com)
//   - openai:    any OpenAI-compatible /chat/completions endpoint (OpenAI, OpenRouter, a local
//                Ollama or LM Studio server, …)
//
// Privacy: the model's provider receives the transaction description, merchant, date and amount,
// plus the list of category names. Nothing is sent unless you press "Ask AI". An optional web
// search (websearch.go) can add what the web says about the merchant.

const (
	llmProviderKey = "llm_provider" // "" (off) | "anthropic" | "openai"
	llmBaseURLKey  = "llm_base_url"
	llmModelKey    = "llm_model"
	llmAPIKeyKey   = "llm_api_key" // stored encrypted

	llmAnthropicURL     = "https://api.anthropic.com"
	llmAnthropicModel   = "claude-opus-5-5"
	llmAnthropicVersion = "2026-10-21"
	llmOpenAIURL        = "https://api.openai.com/v1"
)

// Models that support the API's server-side refusal fallback ("fallbacks": "default").
var llmFallbackModels = map[string]bool{
	"claude-fable-5-2": true, "claude-fable-5-1": true, "claude-opus-5-5": true, "claude-opus-5": true, "claude-sonnet-5-5": true,
}

type llmConfig struct {
	Provider, BaseURL, Model, APIKey string
}

func (a *app) llmConfig() llmConfig {
	c := llmConfig{
		Provider: a.getSetting(llmProviderKey),
		BaseURL:  a.getSetting(llmBaseURLKey),
		Model:    a.getSetting(llmModelKey),
		APIKey:   a.getSecret(llmAPIKeyKey),
	}
	switch c.Provider {
	case "anthropic":
		if c.BaseURL == "" {
			c.BaseURL = llmAnthropicURL
		}
		if c.Model == "" {
			c.Model = llmAnthropicModel
		}
	case "openai":
		if c.BaseURL == "" {
			c.BaseURL = llmOpenAIURL
		}
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return c
}

// llmEnabled reports whether an AI provider is set up well enough to be asked.
func (a *app) llmEnabled() bool {
	c := a.llmConfig()
	switch c.Provider {
	case "anthropic":
		return c.APIKey != ""
	case "openai":
		return c.Model != "" // local servers often need no key
	}
	return false
}

// llmTxn is what the model is told about a transaction.
type llmTxn struct {
	Name, Merchant, Date string
	Amount               float64 // Plaid sign: positive = money out
	Hints                []suggestion
}

type llmAnswer struct {
	Category   string `json:"category"`
	Confidence int    `json:"confidence"`
	Reason     string `json:"reason"`

	Searched  string `json:"-"` // how the web was searched, e.g. "Brave Search (5 results)"; "" if not
	SearchErr error  `json:"-"` // a failed search; the model was asked without it
}

var llmClient = &http.Client{Timeout: 90 * time.Second}

const llmSystemPrompt = `You categorise personal bank and credit card transactions for a household budgeting app.
Given one transaction, choose the single most likely category from the list provided, using the exact category name.
Use what you know about the merchant (from its name, which may be abbreviated or truncated by the bank) and the amount.
Reply with only a JSON object: {"category": "<one category from the list>", "confidence": <0-100>, "reason": "<one short sentence>"}.`

// Added to the system prompt when search results are included in the question.
const llmSearchResultsNote = `
Web search results about the merchant may be included. Use them to work out what kind of business it is, but they can be about a different business with a similar name, so weigh them against the description and amount.`

// Added to the system prompt when the model has Anthropic's web search tool.
const llmSearchToolNote = `
If you don't recognise the merchant, you may use web search to find out what kind of business it is. Search only for the business name (and its town, if the description includes one). Never put the amount, the date or other transaction details in a search. Finish with the JSON object only.`

func llmPrompt(t llmTxn, cats []string, web string) string {
	var sb strings.Builder
	sb.WriteString("Transaction:\n")
	fmt.Fprintf(&sb, "- Description: %s\n", t.Name)
	if t.Merchant != "" && !strings.EqualFold(t.Merchant, t.Name) {
		fmt.Fprintf(&sb, "- Merchant (from the bank): %s\n", t.Merchant)
	}
	dir := "money out (a purchase or payment)"
	if t.Amount < 0 {
		dir = "money in (a refund, income or transfer in)"
	}
	amt := t.Amount
	if amt < 0 {
		amt = -amt
	}
	fmt.Fprintf(&sb, "- Amount: $%s, %s\n", fmtMoney(amt), dir)
	if t.Date != "" {
		fmt.Fprintf(&sb, "- Date: %s\n", t.Date)
	}
	if len(t.Hints) > 0 {
		sb.WriteString("\nThe app's own guesses (may be wrong):\n")
		for _, h := range t.Hints {
			fmt.Fprintf(&sb, "- %s (%d%%): %s\n", h.Category, h.Score, h.Why)
		}
	}
	sb.WriteString(web)
	sb.WriteString("\nCategories:\n")
	sb.WriteString(strings.Join(cats, "\n"))
	return sb.String()
}

// llmSuggest asks the configured model for the most likely category. The returned category is
// always one of pickableCategories().
func (a *app) llmSuggest(ctx context.Context, t llmTxn) (llmAnswer, error) {
	c := a.llmConfig()
	if c.Provider != "anthropic" && c.Provider != "openai" {
		return llmAnswer{}, errors.New("AI is not set up (Settings → AI)")
	}
	cats := pickableCategories()
	system := llmSystemPrompt

	// A separate web search first, its results added to the question.
	var web, searched string
	var searchErr error
	svc := a.getSetting(llmSearchKey)
	builtIn := svc == "anthropic" && c.Provider == "anthropic"
	if svc != "" && svc != "anthropic" {
		if q := webSearchQuery(t); q != "" {
			sctx, cancel := context.WithTimeout(ctx, 25*time.Second)
			res, err := a.webSearch(sctx, q)
			cancel()
			switch {
			case err != nil:
				searchErr = err
				log.Println("ai web search:", err)
			case len(res) == 0:
				searched = webSearchNames[svc] + " (nothing found)"
			default:
				web = webContext(q, res)
				searched = fmt.Sprintf("%s (%d %s)", webSearchNames[svc], len(res), plural(len(res), "result", "results"))
				system += llmSearchResultsNote
			}
		}
	}
	if builtIn {
		system += llmSearchToolNote
	}
	prompt := llmPrompt(t, cats, web)

	var text string
	var err error
	if c.Provider == "anthropic" {
		var searches int
		text, searches, err = llmAnthropic(ctx, c, system, prompt, cats, builtIn)
		if searches > 0 {
			searched = fmt.Sprintf("%s (%d %s)", webSearchNames["anthropic"], searches, plural(searches, "search", "searches"))
		}
	} else {
		text, err = llmOpenAI(ctx, c, system, prompt)
	}
	if err != nil {
		return llmAnswer{}, err
	}
	ans, err := parseLLMAnswer(text, cats)
	ans.Searched, ans.SearchErr = searched, searchErr
	return ans, err
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// parseLLMAnswer pulls the JSON object out of the model's reply (tolerating code fences or a
// sentence around it) and maps the category onto a real one.
func parseLLMAnswer(text string, cats []string) (llmAnswer, error) {
	var ans llmAnswer
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if i < 0 || j <= i || json.Unmarshal([]byte(text[i:j+1]), &ans) != nil {
		return llmAnswer{}, errors.New("the AI's reply wasn't in the expected format")
	}
	want := strings.TrimSpace(ans.Category)
	ans.Category = ""
	for _, c := range cats {
		if strings.EqualFold(c, want) {
			ans.Category = c
			break
		}
	}
	if ans.Category == "" {
		if m := matchCategories(want); len(m) == 1 {
			ans.Category = m[0]
		}
	}
	if ans.Category == "" {
		return llmAnswer{}, fmt.Errorf("the AI suggested “%s”, which isn't one of your categories", want)
	}
	ans.Confidence = min(max(ans.Confidence, 0), 100)
	ans.Reason = strings.TrimSpace(ans.Reason)
	return ans, nil
}

// llmPost sends a JSON request and decodes the JSON reply. Errors never include the API key, and
// transport errors are stripped of the URL.
func llmPost(ctx context.Context, endpoint string, headers map[string]string, in, out any) error {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid AI address")
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := llmClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("couldn't reach the AI: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			return fmt.Errorf("AI error (HTTP %d): %s", resp.StatusCode, e.Error.Message)
		}
		return fmt.Errorf("AI error (HTTP %d)", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errors.New("unexpected reply from the AI")
	}
	return nil
}

// llmAnthropic asks Claude. With webSearch, Claude may search the web itself (Anthropic runs the
// searches); it also returns how many searches were made.
func llmAnthropic(ctx context.Context, c llmConfig, system, prompt string, cats []string, webSearch bool) (string, int, error) {
	oc := map[string]any{"effort": "low"} // a quick classification
	req := map[string]any{
		"model":         c.Model,
		"max_tokens":    4000,
		"system":        system,
		"output_config": oc,
	}
	if webSearch {
		// Search results come back with citations, which can't be combined with a JSON schema, so
		// the answer's format comes from the prompt alone (parseLLMAnswer copes with extra text).
		req["max_tokens"] = 16000
		req["tools"] = []any{anthropicSearchTool(c.Model)}
	} else {
		oc["format"] = map[string]any{
			"type": "json_schema",
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"category":   map[string]any{"type": "string", "enum": cats},
					"confidence": map[string]any{"type": "integer"},
					"reason":     map[string]any{"type": "string"},
				},
				"required":             []string{"category", "confidence", "reason"},
				"additionalProperties": false,
			},
		}
	}
	h := map[string]string{"x-api-key": c.APIKey, "anthropic-version": llmAnthropicVersion}
	if llmFallbackModels[c.Model] {
		req["fallbacks"] = "default"
		h["anthropic-beta"] = "server-side-fallback-2026-07-01"
	}
	messages := []map[string]any{{"role": "user", "content": prompt}}
	searches := 0
	// A long server-side search can pause the turn ("pause_turn"); sending the reply back resumes it.
	for range 3 {
		req["messages"] = messages
		var out struct {
			StopReason string            `json:"stop_reason"`
			Content    []json.RawMessage `json:"content"`
		}
		if err := llmPost(ctx, c.BaseURL+"/v1/messages", h, req, &out); err != nil {
			return "", searches, err
		}
		var sb strings.Builder
		for _, raw := range out.Content {
			var b struct {
				Type string `json:"type"`
				Name string `json:"name"`
				Text string `json:"text"`
			}
			json.Unmarshal(raw, &b)
			switch {
			case b.Type == "text":
				sb.WriteString(b.Text)
			case b.Type == "server_tool_use" && b.Name == "web_search":
				searches++
			}
		}
		switch out.StopReason {
		case "refusal":
			return "", searches, errors.New("the AI declined to answer")
		case "pause_turn":
			messages = append(messages, map[string]any{"role": "assistant", "content": out.Content})
			continue
		}
		return sb.String(), searches, nil
	}
	return "", searches, errors.New("the AI took too long searching the web")
}

func llmOpenAI(ctx context.Context, c llmConfig, system, prompt string) (string, error) {
	req := map[string]any{
		"model": c.Model,
		"messages": []map[string]any{
			{"role": "system", "content": system},
			{"role": "user", "content": prompt},
		},
	}
	h := map[string]string{}
	if c.APIKey != "" {
		h["Authorization"] = "Bearer " + c.APIKey
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := llmPost(ctx, c.BaseURL+"/chat/completions", h, req, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", errors.New("the AI returned no answer")
	}
	return out.Choices[0].Message.Content, nil
}

// ---- settings handler ------------------------------------------------------------

func (a *app) saveLLM(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) { settingsRedirect(w, r, "ai", msg, true) }
	if r.FormValue("remove") == "on" {
		for _, k := range []string{llmProviderKey, llmBaseURLKey, llmModelKey, llmAPIKeyKey, llmSearchKey, llmSearchURLKey,
			llmSearchKeyPrefix + "brave", llmSearchKeyPrefix + "tavily"} {
			a.setSetting(k, "")
		}
		settingsRedirect(w, r, "ai", "AI settings removed.", false)
		return
	}
	provider := r.FormValue("provider")
	base := strings.TrimSpace(r.FormValue("base_url"))
	model := strings.TrimSpace(r.FormValue("model"))
	key := strings.TrimSpace(r.FormValue("api_key"))
	switch provider {
	case "":
		a.setSetting(llmProviderKey, "")
		settingsRedirect(w, r, "ai", "Saved. AI is switched off.", false)
		return
	case "anthropic", "openai":
	default:
		fail("Choose an AI provider.")
		return
	}
	if base != "" {
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			fail("The API address should look like https://api.example.com/v1 or http://localhost:11434/v1.")
			return
		}
	}
	if provider == "openai" && model == "" {
		fail("Enter the model name, for example gpt-5-mini or llama3.2.")
		return
	}
	if provider == "anthropic" && key == "" && a.getSetting(llmAPIKeyKey) == "" {
		fail("Paste your Anthropic API key.")
		return
	}
	search := r.FormValue("search")
	searchURL := strings.TrimSpace(r.FormValue("search_url"))
	searchKey := strings.TrimSpace(r.FormValue("search_key"))
	switch search {
	case "":
	case "anthropic":
		if provider != "anthropic" {
			fail("Anthropic web search works only with the Anthropic provider. Choose Brave, Tavily or SearXNG instead.")
			return
		}
	case "brave", "tavily":
		if searchKey == "" && a.getSetting(llmSearchKeyPrefix+search) == "" {
			fail("Paste your " + webSearchNames[search] + " API key.")
			return
		}
	case "searxng":
		u, err := url.Parse(searchURL)
		if searchURL == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			fail("Enter your SearXNG address, for example http://localhost:8888.")
			return
		}
	default:
		fail("Choose a web search option.")
		return
	}
	if key != "" { // blank means "keep the saved key"
		if err := a.setSecret(llmAPIKeyKey, key); err != nil {
			fail(err.Error())
			return
		}
	}
	if searchKey != "" && (search == "brave" || search == "tavily") {
		if err := a.setSecret(llmSearchKeyPrefix+search, searchKey); err != nil {
			fail(err.Error())
			return
		}
	}
	a.setSetting(llmProviderKey, provider)
	a.setSetting(llmBaseURLKey, base)
	a.setSetting(llmModelKey, model)
	a.setSetting(llmSearchKey, search)
	if search == "searxng" {
		a.setSetting(llmSearchURLKey, searchURL)
	}

	if r.FormValue("test") != "on" {
		settingsRedirect(w, r, "ai", "AI settings saved.", false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	ans, err := a.llmSuggest(ctx, llmTxn{Name: "WOOLWORTHS 1234 SYDNEY", Amount: 82.40, Date: time.Now().Format("2006-01-02")})
	if err != nil {
		fail("Saved, but the test question failed: " + err.Error())
		return
	}
	msg := fmt.Sprintf("AI connected. Test: “WOOLWORTHS 1234 SYDNEY” → %s.", ans.Category)
	switch {
	case ans.SearchErr != nil:
		fail(msg + " But the web search failed: " + ans.SearchErr.Error())
		return
	case ans.Searched != "":
		msg += " Web search: " + ans.Searched + "."
	case search == "anthropic":
		msg += " (The model didn't need to search the web for this one.)"
	}
	settingsRedirect(w, r, "ai", msg, false)
}
