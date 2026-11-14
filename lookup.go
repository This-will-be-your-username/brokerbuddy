package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Online merchant lookup via Wikipedia's free API (no key, no cost).
// Privacy: only the cleaned merchant name (e.g. "bunnings") is sent -- never amounts, dates,
// accounts or the full bank narrative. It runs only when the user clicks the button.
//
// Limits: Wikipedia knows chains and well-known businesses, not the corner café. Results are
// marked "lookup" in the UI so you can review them; unknown merchants stay Uncategorized
// for you to fix once (then "make rule" remembers it).

var lookupClient = &http.Client{Timeout: 15 * time.Second}

// description keywords -> category, first match wins (most specific first).
var kindKeywords = []struct {
	words []string
	cat   string
}{
	{[]string{"casino", "bookmaker", "betting", "gambling", "lottery", "wagering"}, "Gambling"},
	{[]string{"buy now, pay later", "buy now pay later"}, "Buy Now Pay Later"},
	{[]string{"supermarket", "grocery"}, "Groceries"},
	{[]string{"petrol", "fuel", "service station", "convenience store", "toll road", "public transport", "taxi", "ride-hailing", "rideshare"}, "Transport & Fuel"},
	{[]string{"health insurance", "insurance", "insurer"}, "Insurance"},
	{[]string{"pharmacy", "chemist", "hospital", "medical", "dental", "optical", "health care", "healthcare", "fitness", "gym"}, "Health & Medical"},
	{[]string{"telecommunications", "electricity", "energy retailer", "utility", "internet service", "mobile network", "water"}, "Utilities & Internet"},
	{[]string{"hardware", "home improvement", "furniture"}, "Home & Strata"},
	{[]string{"university", "school", "childcare", "education"}, "Education & Childcare"},
	{[]string{"airline", "hotel", "travel agency", "car rental", "accommodation", "online travel"}, "Travel & Holidays"},
	{[]string{"streaming", "subscription", "software"}, "Subscriptions"},
	{[]string{"cinema", "theatre", "video game", "amusement", "ticketing", "entertainment"}, "Entertainment"},
	{[]string{"liquor", "bottle shop", "tobacco"}, "Alcohol & Tobacco"},
	{[]string{"restaurant", "fast food", "cafe", "café", "coffee", "pizza", "bakery", "takeaway", "food delivery", "pub chain"}, "Dining & Takeaway"},
	{[]string{"pet "}, "Pets"},
	{[]string{"department store", "clothing", "fashion", "retailer", "retail chain", "e-commerce", "electronics"}, "Shopping & Clothing"},
	{[]string{"salon", "cosmetics", "beauty"}, "Personal Care"},
	{[]string{"charity", "non-profit", "nonprofit"}, "Gifts & Donations"},
}

func kindToCategory(text string) string {
	t := strings.ToLower(text)
	for _, k := range kindKeywords {
		for _, w := range k.words {
			if strings.Contains(t, w) {
				return k.cat
			}
		}
	}
	return ""
}

func wikiGet(u string, out any) error {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "BrokerBuddy/1.0 (personal budgeting tool; merchant name lookups only)")
	resp, err := lookupClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("wikipedia: HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// lookupMerchant returns (category, detail). Empty category = nothing trustworthy found.
func lookupMerchant(key string) (cat, detail, usedQuery string, err error) {
	queries := []string{key}
	if f := strings.Fields(key)[0]; f != key && len(f) >= 4 { // suburb/location noise: retry on the first word alone
		queries = append(queries, f)
	}
	for i, q := range queries {
		if i > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		c, d, e := lookupOne(q)
		if e != nil {
			return "", "", "", e
		}
		if c != "" {
			return c, d, q, nil
		}
		detail = d
	}
	return "", detail, "", nil
}

func lookupOne(key string) (string, string, error) {
	var sr struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	q := url.Values{"action": {"query"}, "list": {"search"}, "srsearch": {key + " Australia"}, "srlimit": {"3"}, "format": {"json"}}
	if err := wikiGet("https://en.wikipedia.org/w/api.php?"+q.Encode(), &sr); err != nil {
		return "", "", err
	}
	first := strings.Fields(key)[0]
	for _, hit := range sr.Query.Search {
		// Guard against unrelated results: the article title must contain the merchant's first word.
		if !strings.Contains(" "+norm(hit.Title)+" ", " "+first+" ") {
			continue
		}
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
		if cat := kindToCategory(sum.Description + " " + ex); cat != "" {
			return cat, hit.Title + " — " + sum.Description, nil
		}
		return "", hit.Title + " — " + sum.Description, nil
	}
	return "", "", nil
}

type lookupView struct{ Key, Category, Detail string }

func (a *app) lookupList() []lookupView {
	rows, err := a.db.Query(`SELECT key, category, detail FROM merchant_cache WHERE category<>'' ORDER BY looked_up_at DESC LIMIT 50`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []lookupView
	for rows.Next() {
		var v lookupView
		rows.Scan(&v.Key, &v.Category, &v.Detail)
		out = append(out, v)
	}
	return out
}

// unknownMerchants: merchant keys of unmatched spending, biggest spend first, skipping already-looked-up ones.
func (a *app) unknownMerchants(limit int) ([]string, error) {
	cache, err := loadCache(a.db)
	if err != nil {
		return nil, err
	}
	rows, err := a.db.Query(`SELECT name, SUM(amount) FROM transactions WHERE cat_source='' AND amount>0 AND manual=0 GROUP BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	spend := map[string]float64{}
	for rows.Next() {
		var n string
		var s float64
		rows.Scan(&n, &s)
		if k := merchantKey(n); k != "" {
			if _, done := cache[k]; !done {
				spend[k] += s
			}
		}
	}
	keys := make([]string, 0, len(spend))
	for k := range spend {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return spend[keys[i]] > spend[keys[j]] })
	if len(keys) > limit {
		keys = keys[:limit]
	}
	return keys, nil
}

func (a *app) lookupUnknown(w http.ResponseWriter, r *http.Request) {
	keys, err := a.unknownMerchants(12)
	if err != nil {
		fail(w, err, 500)
		return
	}
	found := 0
	for i, k := range keys {
		if i > 0 {
			time.Sleep(700 * time.Millisecond) // be polite to the free API
		}
		cat, detail, used, err := lookupMerchant(k)
		if err != nil {
			log.Println("lookup", k, err)
			continue // transient failure: don't cache, retry next time
		}
		if cat != "" {
			found++
		}
		save := func(key string) {
			a.db.Exec(`INSERT INTO merchant_cache(key,category,source,detail,looked_up_at) VALUES(?,?,?,?,?)
				ON CONFLICT(key) DO UPDATE SET category=excluded.category, source=excluded.source, detail=excluded.detail, looked_up_at=excluded.looked_up_at`,
				key, cat, "wikipedia", detail, time.Now().UTC().Format(time.RFC3339))
		}
		save(k)
		if used != "" && used != k {
			save(used) // so the same business in another suburb matches too
		}
	}
	if err := recategorizeAll(a.db); err != nil {
		fail(w, err, 500)
		return
	}
	msg := fmt.Sprintf("Looked up %d merchants, categorised %d. Review the “Looked up” list.", len(keys), found)
	if len(keys) == 0 {
		msg = "No unmatched merchants left to look up."
	}
	http.Redirect(w, r, "/settings?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (a *app) lookupDelete(w http.ResponseWriter, r *http.Request) {
	// Keep a blank entry so a rejected guess isn't looked up again.
	a.db.Exec(`UPDATE merchant_cache SET category='', detail='rejected by user' WHERE key=?`, r.FormValue("key"))
	recategorizeAll(a.db)
	http.Redirect(w, r, "/settings#categorisation", http.StatusSeeOther)
}

func (a *app) recategorize(w http.ResponseWriter, r *http.Request) {
	if err := recategorizeAll(a.db); err != nil {
		fail(w, err, 500)
		return
	}
	http.Redirect(w, r, "/settings?msg=Recategorised", http.StatusSeeOther)
}
