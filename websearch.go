package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Optional web search for "Ask AI". Before asking the model, the app can search the web for the
// merchant and add the top results to the prompt, so the model knows what an unfamiliar business
// actually is. Or, with Anthropic, the model can search the web itself (Anthropic's web search tool).
//
// Privacy: the search services below receive only the cleaned merchant name (as the Wikipedia
// lookup does), never amounts, dates or the full bank description. With Anthropic's own search the
// model writes the queries; it is told to search for the business name only.

const (
	llmSearchKey    = "llm_search"     // "" (off) | "anthropic" | "brave" | "tavily" | "searxng"
	llmSearchURLKey = "llm_search_url" // SearXNG address
	// The API key for each search service is stored encrypted under llmSearchKeyPrefix + service,
	// so switching between services doesn't send one service's key to another.
	llmSearchKeyPrefix = "llm_search_key_"

	webSearchResults  = 5
	webSnippetMaxLen  = 300
	braveSearchURL    = "https://api.search.brave.com/res/v1/web/search"
	tavilySearchURL   = "https://api.tavily.com/search"
	anthropicMaxUses  = 3
	webSearchCountry  = "AU"
	webSearchLocation = "Australia"
)

var webSearchNames = map[string]string{
	"anthropic": "Anthropic web search",
	"brave":     "Brave Search",
	"tavily":    "Tavily",
	"searxng":   "SearXNG",
}

// Overridable in tests.
var (
	braveEndpoint  = braveSearchURL
	tavilyEndpoint = tavilySearchURL
)

var webSearchClient = &http.Client{Timeout: 20 * time.Second}

type webResult struct{ Title, URL, Snippet string }

// webSearchQuery is what gets searched for: the cleaned merchant name plus the country, or "" when
// nothing useful is left after cleaning.
func webSearchQuery(t llmTxn) string {
	key := ""
	if t.Merchant != "" {
		key = merchantKey(t.Merchant)
	}
	if key == "" {
		key = merchantKey(t.Name)
	}
	if key == "" {
		return ""
	}
	return key + " " + webSearchLocation
}

// webSearch runs the configured separate search service. Anthropic's built-in search is not handled
// here: that happens inside the model request.
func (a *app) webSearch(ctx context.Context, query string) ([]webResult, error) {
	svc := a.getSetting(llmSearchKey)
	key := a.getSecret(llmSearchKeyPrefix + svc)
	var res []webResult
	var err error
	switch svc {
	case "brave":
		res, err = braveSearch(ctx, key, query)
	case "tavily":
		res, err = tavilySearch(ctx, key, query)
	case "searxng":
		res, err = searxngSearch(ctx, a.getSetting(llmSearchURLKey), query)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", webSearchNames[svc], err)
	}
	if len(res) > webSearchResults {
		res = res[:webSearchResults]
	}
	for i := range res {
		res[i].Title = strings.TrimSpace(stripTags(res[i].Title))
		res[i].Snippet = strings.TrimSpace(stripTags(res[i].Snippet))
		if r := []rune(res[i].Snippet); len(r) > webSnippetMaxLen {
			res[i].Snippet = string(r[:webSnippetMaxLen]) + "…"
		}
	}
	return res, nil
}

// webDo sends a search request. Errors never include the API key or the URL (which may hold it).
func webDo(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "BrokerBuddy/1.0 (personal budgeting tool; merchant name searches only)")
	resp, err := webSearchClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("couldn't reach the search service: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode/100 != 2 {
		switch resp.StatusCode {
		case 401, 403:
			return fmt.Errorf("the search API key was refused (HTTP %d)", resp.StatusCode)
		case 429:
			return errors.New("search limit reached (HTTP 429)")
		}
		return fmt.Errorf("search error (HTTP %d)", resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errors.New("unexpected reply from the search service (for SearXNG, enable the json format)")
	}
	return nil
}

func braveSearch(ctx context.Context, key, query string) ([]webResult, error) {
	if key == "" {
		return nil, errors.New("no API key")
	}
	q := url.Values{"q": {query}, "count": {fmt.Sprint(webSearchResults)}, "country": {webSearchCountry}, "safesearch": {"moderate"}}
	req, err := http.NewRequestWithContext(ctx, "GET", braveEndpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Subscription-Token", key)
	var out struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := webDo(req, &out); err != nil {
		return nil, err
	}
	var res []webResult
	for _, r := range out.Web.Results {
		res = append(res, webResult{r.Title, r.URL, r.Description})
	}
	return res, nil
}

func tavilySearch(ctx context.Context, key, query string) ([]webResult, error) {
	if key == "" {
		return nil, errors.New("no API key")
	}
	body, _ := json.Marshal(map[string]any{"query": query, "max_results": webSearchResults, "search_depth": "basic"})
	req, err := http.NewRequestWithContext(ctx, "POST", tavilyEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := webDo(req, &out); err != nil {
		return nil, err
	}
	var res []webResult
	for _, r := range out.Results {
		res = append(res, webResult{r.Title, r.URL, r.Content})
	}
	return res, nil
}

func searxngSearch(ctx context.Context, base, query string) ([]webResult, error) {
	if base == "" {
		return nil, errors.New("no address")
	}
	q := url.Values{"q": {query}, "format": {"json"}, "safesearch": {"1"}}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+"/search?"+q.Encode(), nil)
	if err != nil {
		return nil, errors.New("invalid address")
	}
	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := webDo(req, &out); err != nil {
		return nil, err
	}
	var res []webResult
	for _, r := range out.Results {
		res = append(res, webResult{r.Title, r.URL, r.Content})
	}
	return res, nil
}

// stripTags removes simple HTML markup (search snippets often contain <strong> highlights).
func stripTags(s string) string {
	var sb strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>' && in:
			in = false
		case !in:
			sb.WriteRune(r)
		}
	}
	return strings.NewReplacer("&amp;", "&", "&quot;", `"`, "&#39;", "'", "&lt;", "<", "&gt;", ">", "&nbsp;", " ").Replace(sb.String())
}

// webContext formats search results for the prompt.
func webContext(query string, res []webResult) string {
	if len(res) == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "\nWeb search results for “%s” (they may be about a different business with a similar name):\n", query)
	for i, r := range res {
		fmt.Fprintf(&sb, "%d. %s", i+1, r.Title)
		if u, err := url.Parse(r.URL); err == nil && u.Host != "" {
			fmt.Fprintf(&sb, " (%s)", strings.TrimPrefix(u.Host, "www."))
		}
		if r.Snippet != "" {
			sb.WriteString(": " + r.Snippet)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// anthropicSearchTool is Anthropic's server-side web search tool, in the newest version the model
// supports (the 2026-02-09 version filters results with code before they reach the model).
func anthropicSearchTool(model string) map[string]any {
	typ := "web_search_20250305"
	for _, p := range []string{"claude-fable-", "claude-mythos-", "claude-opus-5", "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-5", "claude-sonnet-4-6"} {
		if strings.HasPrefix(model, p) {
			typ = "web_search_20260209"
			break
		}
	}
	return map[string]any{
		"type":          typ,
		"name":          "web_search",
		"max_uses":      anthropicMaxUses,
		"user_location": map[string]any{"type": "approximate", "country": webSearchCountry},
	}
}
