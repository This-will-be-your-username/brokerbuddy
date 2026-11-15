package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Suggestions for transactions the app could not categorise with confidence. Local suggestions
// come from the app's own knowledge (rules, dictionary, your history); "search online" adds
// candidates from Wikipedia. Privacy: online searches send ONLY the cleaned merchant name,
// never amounts, dates, accounts or the full narrative, and only when the button is pressed.

type suggestion struct {
	Category string `json:"category"`
	Score    int    `json:"score"` // 0-100, how likely this category is right
	Why      string `json:"why"`
	Source   string `json:"source"` // best-guess | history | name | common | web
}

// kindCandidates returns every category a piece of descriptive text points to, in keyword order.
func kindCandidates(text string) []string {
	t := strings.ToLower(text)
	var out []string
	for _, k := range kindKeywords {
		for _, w := range k.words {
			if strings.Contains(t, w) {
				out = append(out, k.cat)
				break
			}
		}
	}
	return out
}

// mergeSuggestions keeps the best score per category, joins the reasons, and orders by score.
func mergeSuggestions(lists ...[]suggestion) []suggestion {
	best := map[string]suggestion{}
	for _, l := range lists {
		for _, s := range l {
			if s.Category == "" || s.Category == "Uncategorized" {
				continue
			}
			old, ok := best[s.Category]
			switch {
			case !ok:
				best[s.Category] = s
			case s.Score > old.Score:
				if old.Why != "" && !strings.Contains(s.Why, old.Why) {
					s.Why += "; " + old.Why
				}
				best[s.Category] = s
			case s.Why != "" && !strings.Contains(old.Why, s.Why):
				old.Why += "; " + s.Why
				best[s.Category] = old
			}
		}
	}
	out := make([]suggestion, 0, len(best))
	for _, s := range best {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Category < out[j].Category
	})
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func (a *app) localSuggestions(name, merchant string, amount float64) ([]suggestion, error) {
	c, err := newClassifier(a.db)
	if err != nil {
		return nil, err
	}
	var out []suggestion
	if r := c.classifyFull(name, merchant, "", amount); r.Cat != "" && r.Cat != "Uncategorized" {
		out = append(out, suggestion{r.Cat, r.Conf, "the app's best guess (" + r.Src + ")", "best-guess"})
	}
	toks := descriptorTokens(name)
	if cat, ok := suffixGuess(toks); ok {
		out = append(out, suggestion{cat, 45, "the name ends in a business word", "name"})
	}
	// what you chose for other transactions containing the same distinctive word
	type cn struct {
		cat string
		n   int
	}
	for _, tok := range toks {
		if len(tok) < 4 {
			continue
		}
		rows, err := a.db.Query(`SELECT category, COUNT(*) FROM transactions WHERE (manual=1 OR confidence>=80) AND category<>'Uncategorized' AND LOWER(name) LIKE ? GROUP BY category`, "%"+tok+"%")
		if err != nil {
			return nil, err
		}
		total := 0
		var cs []cn
		for rows.Next() {
			var x cn
			rows.Scan(&x.cat, &x.n)
			cs = append(cs, x)
			total += x.n
		}
		rows.Close()
		for _, x := range cs {
			score := 25 + 35*x.n/total
			if x.n < 2 {
				score -= 10
			}
			out = append(out, suggestion{x.cat, score, "your other transactions containing “" + tok + "”", "history"})
		}
	}
	out = mergeSuggestions(out)
	if len(out) < 3 { // never leave the list empty: offer the categories you use most
		rows, err := a.db.Query(`SELECT category, COUNT(*) c FROM transactions WHERE category<>'Uncategorized' AND amount>0 GROUP BY category ORDER BY c DESC LIMIT 4`)
		if err == nil {
			var common []suggestion
			for rows.Next() {
				var cat string
				var n int
				rows.Scan(&cat, &n)
				common = append(common, suggestion{cat, 10, "one of your most used categories", "common"})
			}
			rows.Close()
			out = mergeSuggestions(out, common)
		}
	}
	return out, nil
}

// webSuggestions searches Wikipedia for the merchant name and turns each relevant article into candidates.
func webSuggestions(key string) ([]suggestion, error) {
	words := strings.Fields(key)
	if len(words) == 0 {
		return nil, nil
	}
	queries := []string{key}
	if len(words) > 2 {
		queries = append(queries, strings.Join(words[:2], " "))
	}
	if len(words[0]) >= 4 && len(words) > 1 {
		queries = append(queries, words[0])
	}
	first := words[0]
	seen := map[string]bool{}
	var out []suggestion
	for qi, q := range queries {
		if qi > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		var sr struct {
			Query struct {
				Search []struct {
					Title string `json:"title"`
				} `json:"search"`
			} `json:"query"`
		}
		v := url.Values{"action": {"query"}, "list": {"search"}, "srsearch": {q + " Australia"}, "srlimit": {"5"}, "format": {"json"}}
		if err := wikiGet("https://en.wikipedia.org/w/api.php?"+v.Encode(), &sr); err != nil {
			return out, err
		}
		for _, hit := range sr.Query.Search {
			if seen[hit.Title] || !strings.Contains(" "+norm(hit.Title)+" ", " "+first+" ") {
				continue
			}
			seen[hit.Title] = true
			time.Sleep(500 * time.Millisecond)
			var sum struct {
				Description string `json:"description"`
				Extract     string `json:"extract"`
			}
			if err := wikiGet("https://en.wikipedia.org/api/rest_v1/page/summary/"+url.PathEscape(strings.ReplaceAll(hit.Title, " ", "_")), &sum); err != nil {
				continue
			}
			ex := sum.Extract
			if len(ex) > 300 {
				ex = ex[:300]
			}
			base := 40
			if norm(hit.Title) == key {
				base = 58
			}
			for i, cat := range kindCandidates(sum.Description + " " + ex) {
				if i >= 3 {
					break
				}
				out = append(out, suggestion{cat, base - 8*i, "Wikipedia: " + hit.Title + " — " + sum.Description, "web"})
			}
		}
	}
	return mergeSuggestions(out), nil
}

// webSuggestCached remembers the answer for a merchant so repeat clicks (and other transactions
// from the same merchant) don't search again. Failures are not cached.
func (a *app) webSuggestCached(key string) ([]suggestion, error) {
	var js string
	err := a.db.QueryRow(`SELECT json FROM suggestion_cache WHERE key=?`, key).Scan(&js)
	if err == nil {
		var s []suggestion
		if json.Unmarshal([]byte(js), &s) == nil {
			return s, nil
		}
	} else if err != sql.ErrNoRows {
		return nil, err
	}
	s, err := webSearch(key)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(s)
	a.db.Exec(`INSERT INTO suggestion_cache(key,json,at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET json=excluded.json, at=excluded.at`,
		key, string(b), time.Now().UTC().Format(time.RFC3339))
	return s, nil
}

// webSearch is a variable so tests can run without the network.
var webSearch = webSuggestions

// POST /api/suggest  id=<transaction>  [web=1]
func (a *app) suggestPage(w http.ResponseWriter, r *http.Request) {
	var name, merchant string
	var amount float64
	if err := a.db.QueryRow(`SELECT name, merchant, amount FROM transactions WHERE plaid_txn_id=?`, r.FormValue("id")).Scan(&name, &merchant, &amount); err != nil {
		fail(w, errBad, 404)
		return
	}
	opts, err := a.localSuggestions(name, merchant, amount)
	if err != nil {
		fail(w, err, 500)
		return
	}
	key := merchantKey(name)
	resp := map[string]any{"merchant": key}
	if r.FormValue("web") == "1" && key != "" {
		web, err := a.webSuggestCached(key)
		switch {
		case err != nil:
			resp["error"] = "The online search failed (" + err.Error() + "). Local suggestions are shown."
		case len(web) == 0:
			resp["note"] = "Nothing found online for “" + key + "”. Pick a category or type your own."
		default:
			opts = mergeSuggestions(opts, web)
		}
		resp["searched"] = true
	}
	resp["options"] = opts
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
