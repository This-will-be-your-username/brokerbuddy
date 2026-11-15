package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Filters for the Transactions page. Every value comes from the query string, is validated, and
// reaches SQL only as a bound parameter (or as one of a fixed set of clauses).

const txnPageSize = 200

type txnFilter struct {
	Q        string
	Accounts []string // account ids
	Cats     []string
	Group    string
	Conf     string // confirmed|high|medium|low|unknown|review
	Dir      string // out|in
	How      string // manual|rule|learned|builtin|lookup|hint|fuzzy|history|cue|none
	Sort     string
	From, To string // YYYY-MM-DD
	Min, Max string // dollar amounts, compared with the absolute value
	Ign      string // "" = hide ignored credit card repayments, show = include them, only = just those
	Page     int
}

var (
	validConf = map[string]bool{"confirmed": true, "high": true, "medium": true, "low": true, "unknown": true, "review": true}
	validHow  = map[string]string{"manual": "Set by you", "rule": "Your rules", "learned": "Learned from your edits", "builtin": "Built-in merchant list",
		"lookup": "Online lookup", "hint": "Bank's category", "fuzzy": "Typo of a known brand", "history": "Similar to your history", "cue": "Wording cue", "none": "Not matched"}
	sortOrders = map[string]struct{ Label, SQL string }{
		"":            {"Newest first", "t.date DESC, t.plaid_txn_id"},
		"date_asc":    {"Oldest first", "t.date ASC, t.plaid_txn_id"},
		"amount_desc": {"Largest amount", "ABS(t.amount) DESC, t.date DESC"},
		"amount_asc":  {"Smallest amount", "ABS(t.amount) ASC, t.date DESC"},
		"name":        {"Description A–Z", "lower(t.name), t.date DESC"},
		"confidence":  {"Least sure first", "t.confidence ASC, lower(t.name), t.date DESC"},
	}
)

func cleanDate(s string) string {
	if _, err := time.Parse("2006-01-02", s); err == nil {
		return s
	}
	return ""
}

func cleanMoney(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.TrimPrefix(strings.TrimSpace(s), "$"), ",", ""))
	if v, err := strconv.ParseFloat(s, 64); err == nil && v >= 0 {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func parseTxnFilter(q url.Values) txnFilter {
	f := txnFilter{Q: strings.TrimSpace(q.Get("q")), Group: q.Get("group"), From: cleanDate(q.Get("from")), To: cleanDate(q.Get("to")),
		Min: cleanMoney(q.Get("min")), Max: cleanMoney(q.Get("max")), Page: 1}
	for _, v := range q["account"] {
		if v != "" {
			f.Accounts = append(f.Accounts, v)
		}
	}
	for _, v := range q["category"] {
		if _, ok := groupOf[v]; ok { // only categories that exist
			f.Cats = append(f.Cats, v)
		}
	}
	if _, ok := groupCats()[f.Group]; !ok {
		f.Group = ""
	}
	if c := q.Get("confidence"); validConf[c] {
		f.Conf = c
	}
	if d := q.Get("dir"); d == "out" || d == "in" {
		f.Dir = d
	}
	if h := q.Get("how"); validHow[h] != "" {
		f.How = h
	}
	if _, ok := sortOrders[q.Get("sort")]; ok {
		f.Sort = q.Get("sort")
	}
	if v := q.Get("ignored"); v == "show" || v == "only" {
		f.Ign = v
	}
	if n, err := strconv.Atoi(q.Get("page")); err == nil && n > 1 {
		f.Page = n
	}
	return f
}

// groupCats lists each group's categories.
func groupCats() map[string][]string {
	m := map[string][]string{}
	for _, c := range taxonomy {
		m[c.Group] = append(m[c.Group], c.Name)
	}
	return m
}

// values returns the filter as a query string (without the page).
func (f txnFilter) values() url.Values {
	v := url.Values{}
	set := func(k, s string) {
		if s != "" {
			v.Set(k, s)
		}
	}
	set("q", f.Q)
	for _, a := range f.Accounts {
		v.Add("account", a)
	}
	for _, c := range f.Cats {
		v.Add("category", c)
	}
	set("group", f.Group)
	set("confidence", f.Conf)
	set("dir", f.Dir)
	set("how", f.How)
	set("sort", f.Sort)
	set("from", f.From)
	set("to", f.To)
	set("min", f.Min)
	set("max", f.Max)
	set("ignored", f.Ign)
	return v
}

// url builds a link to the same filters with a change applied (and back to page 1).
func (f txnFilter) url(change func(*txnFilter)) string {
	g := f
	g.Accounts, g.Cats = append([]string(nil), f.Accounts...), append([]string(nil), f.Cats...)
	if change != nil {
		change(&g)
	}
	v := g.values()
	if g.Page > 1 {
		v.Set("page", strconv.Itoa(g.Page))
	}
	if len(v) == 0 {
		return "/transactions"
	}
	return "/transactions?" + v.Encode()
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// where returns the SQL conditions and arguments. withConf=false leaves out the confidence filter
// so the chips can show how many transactions sit at each level.
func (f txnFilter) where(withConf bool) ([]string, []any) {
	conds, args := []string{"1=1"}, []any{}
	if f.Q != "" {
		like := "%" + likeEscaper.Replace(f.Q) + "%"
		conds = append(conds, `(t.name LIKE ? ESCAPE '\' OR t.merchant LIKE ? ESCAPE '\')`)
		args = append(args, like, like)
	}
	if len(f.Accounts) > 0 {
		conds = append(conds, "t.account_id IN ("+placeholders(len(f.Accounts))+")")
		for _, a := range f.Accounts {
			args = append(args, a)
		}
	}
	if len(f.Cats) > 0 {
		conds = append(conds, "t.category IN ("+placeholders(len(f.Cats))+")")
		for _, c := range f.Cats {
			args = append(args, c)
		}
	}
	if f.Group != "" {
		cs := groupCats()[f.Group]
		conds = append(conds, "t.category IN ("+placeholders(len(cs))+")")
		for _, c := range cs {
			args = append(args, c)
		}
	}
	switch f.Dir {
	case "out":
		conds = append(conds, "t.amount>0")
	case "in":
		conds = append(conds, "t.amount<0")
	}
	if f.From != "" {
		conds, args = append(conds, "t.date>=?"), append(args, f.From)
	}
	if f.To != "" {
		conds, args = append(conds, "t.date<=?"), append(args, f.To)
	}
	if f.Min != "" {
		v, _ := strconv.ParseFloat(f.Min, 64)
		conds, args = append(conds, "ABS(t.amount)>=?"), append(args, v)
	}
	if f.Max != "" {
		v, _ := strconv.ParseFloat(f.Max, 64)
		conds, args = append(conds, "ABS(t.amount)<=?"), append(args, v)
	}
	switch f.How {
	case "":
	case "manual":
		conds = append(conds, "t.manual=1")
	case "none":
		conds = append(conds, "t.manual=0 AND t.cat_source=''")
	default:
		conds, args = append(conds, "t.manual=0 AND t.cat_source=?"), append(args, f.How)
	}
	switch f.Ign {
	case "":
		conds = append(conds, "t.ignored=0")
	case "only":
		conds = append(conds, "t.ignored=1")
	}
	if withConf {
		if cc := confClause(f.Conf); cc != "" {
			conds = append(conds, cc)
		}
	}
	return conds, args
}

func (f txnFilter) order() string {
	if f.Sort == "" && (f.Conf == "review" || f.Conf == "low" || f.Conf == "unknown" || f.Conf == "medium") {
		return sortOrders["confidence"].SQL // reviewing: least sure first, same merchants together
	}
	return sortOrders[f.Sort].SQL
}

type activeFilter struct{ Label, URL string }

// active lists the filters in effect, each with a link that removes just that one.
func (f txnFilter) active(acctName map[string]string) []activeFilter {
	var out []activeFilter
	add := func(label string, change func(*txnFilter)) { out = append(out, activeFilter{label, f.url(change)}) }
	if f.Q != "" {
		add("Search: “"+f.Q+"”", func(g *txnFilter) { g.Q = "" })
	}
	for _, a := range f.Accounts {
		a := a
		name := acctName[a]
		if name == "" {
			name = a
		}
		add("Account: "+name, func(g *txnFilter) { g.Accounts = without(g.Accounts, a) })
	}
	for _, c := range f.Cats {
		c := c
		add("Category: "+c, func(g *txnFilter) { g.Cats = without(g.Cats, c) })
	}
	if f.Group != "" {
		add("Group: "+f.Group, func(g *txnFilter) { g.Group = "" })
	}
	if f.Conf != "" {
		add("Confidence: "+confChipLabel(f.Conf), func(g *txnFilter) { g.Conf = "" })
	}
	if f.Dir != "" {
		label := map[string]string{"out": "Money out", "in": "Money in"}[f.Dir]
		add("Direction: "+label, func(g *txnFilter) { g.Dir = "" })
	}
	if f.From != "" {
		add("From "+f.From, func(g *txnFilter) { g.From = "" })
	}
	if f.To != "" {
		add("To "+f.To, func(g *txnFilter) { g.To = "" })
	}
	if f.Min != "" {
		add("At least $"+f.Min, func(g *txnFilter) { g.Min = "" })
	}
	if f.Max != "" {
		add("At most $"+f.Max, func(g *txnFilter) { g.Max = "" })
	}
	if f.How != "" {
		add("Categorised by: "+validHow[f.How], func(g *txnFilter) { g.How = "" })
	}
	if f.Ign != "" {
		label := map[string]string{"show": "Including ignored", "only": "Only ignored"}[f.Ign]
		add(label, func(g *txnFilter) { g.Ign = "" })
	}
	return out
}

func confChipLabel(c string) string {
	return map[string]string{"confirmed": "Confirmed", "high": "High", "medium": "Medium", "low": "Low", "unknown": "Unknown", "review": "Needs review"}[c]
}

func without(in []string, drop string) []string {
	var out []string
	for _, s := range in {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}

func (a *app) index(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := parseTxnFilter(q)

	conds, args := f.where(true)
	W := strings.Join(conds, " AND ")

	// matching totals
	var total int
	var out, in float64
	a.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(CASE WHEN t.amount>0 THEN t.amount END),0), COALESCE(-SUM(CASE WHEN t.amount<0 THEN t.amount END),0)
		FROM transactions t WHERE `+W, args...).Scan(&total, &out, &in)
	pages := (total + txnPageSize - 1) / txnPageSize
	if pages == 0 {
		pages = 1
	}
	if f.Page > pages {
		f.Page = pages
	}

	var txns []txnRow
	rows, err := a.db.Query(`SELECT t.plaid_txn_id,t.date,t.name,a.name||CASE WHEN a.mask<>'' THEN ' ••'||a.mask ELSE '' END,t.category,t.amount,t.manual,t.cat_source,t.confidence,t.ignored,t.ignore_override,a.is_card
		FROM transactions t JOIN accounts a ON a.plaid_account_id=t.account_id
		WHERE `+W+fmt.Sprintf(` ORDER BY %s LIMIT %d OFFSET %d`, f.order(), txnPageSize, (f.Page-1)*txnPageSize), args...)
	if err != nil {
		fail(w, err, 500)
		return
	}
	for rows.Next() {
		var t txnRow
		var ign, card int
		rows.Scan(&t.ID, &t.Date, &t.Name, &t.Account, &t.Category, &t.Amount, &t.Manual, &t.Source, &t.Conf, &ign, &t.Override, &card)
		t.Ignored = ign == 1
		if t.Ignored {
			t.IgnoreWhy = ignoreWhy(map[bool]string{true: ignoreCardPayment, false: ignorePaidElsewhere}[card == 1])
		}
		t.Level = confLevel(t.Conf, t.Manual, t.Category)
		t.Why = srcWhy(t.Source, t.Manual)
		txns = append(txns, t)
	}
	rows.Close()

	// confidence chips: counts for everything except the confidence filter itself
	baseConds, baseArgs := f.where(false)
	var nConfirmed, nHigh, nMedium, nLow, nUnknown int
	a.db.QueryRow(`SELECT
		COALESCE(SUM(t.manual=1),0),
		COALESCE(SUM(t.manual=0 AND t.confidence>=80 AND t.category<>'Uncategorized'),0),
		COALESCE(SUM(t.manual=0 AND t.confidence BETWEEN 50 AND 79 AND t.category<>'Uncategorized'),0),
		COALESCE(SUM(t.manual=0 AND t.ignored=0 AND t.confidence BETWEEN 1 AND 49 AND t.category<>'Uncategorized'),0),
		COALESCE(SUM(t.manual=0 AND t.ignored=0 AND (t.confidence=0 OR t.category='Uncategorized')),0)
		FROM transactions t WHERE `+strings.Join(baseConds, " AND "), baseArgs...).Scan(&nConfirmed, &nHigh, &nMedium, &nLow, &nUnknown)
	setConf := func(level string) string { return f.url(func(g *txnFilter) { g.Conf = level }) }
	chips := []confChip{
		{"All", "all", setConf(""), nConfirmed + nHigh + nMedium + nLow + nUnknown, f.Conf == ""},
		{"Needs review", "review", setConf("review"), nLow + nUnknown, f.Conf == "review"},
		{"Unknown", "unknown", setConf("unknown"), nUnknown, f.Conf == "unknown"},
		{"Low", "low", setConf("low"), nLow, f.Conf == "low"},
		{"Medium", "medium", setConf("medium"), nMedium, f.Conf == "medium"},
		{"High", "high", setConf("high"), nHigh, f.Conf == "high"},
		{"Confirmed", "confirmed", setConf("confirmed"), nConfirmed, f.Conf == "confirmed"},
	}

	// accounts for the menu
	var accts []acctOpt
	acctName := map[string]string{}
	if ar, err := a.db.Query(`SELECT plaid_account_id, name||CASE WHEN mask<>'' THEN ' ••'||mask ELSE '' END FROM accounts ORDER BY name`); err == nil {
		for ar.Next() {
			var o acctOpt
			ar.Scan(&o.ID, &o.Name)
			accts = append(accts, o)
			acctName[o.ID] = o.Name
		}
		ar.Close()
	}
	selAcct, selCat := map[string]bool{}, map[string]bool{}
	for _, x := range f.Accounts {
		selAcct[x] = true
	}
	for _, x := range f.Cats {
		selCat[x] = true
	}
	var sorts []struct{ Value, Label string }
	for _, k := range []string{"", "date_asc", "amount_desc", "amount_asc", "name", "confidence"} {
		sorts = append(sorts, struct{ Value, Label string }{k, sortOrders[k].Label})
	}
	var hows []struct{ Value, Label string }
	for _, k := range []string{"manual", "rule", "learned", "builtin", "lookup", "hint", "fuzzy", "history", "cue", "none"} {
		hows = append(hows, struct{ Value, Label string }{k, validHow[k]})
	}
	var groups []string
	for _, g := range []string{gIncome, gEss, gDisc, gDebt, gRisk, gNeut} {
		groups = append(groups, g)
	}
	active := f.active(acctName)
	prevURL, nextURL := "", ""
	if f.Page > 1 {
		prevURL = f.url(func(g *txnFilter) { g.Page = f.Page - 1 })
	}
	if f.Page < pages {
		nextURL = f.url(func(g *txnFilter) { g.Page = f.Page + 1 })
	}
	// how many ignored rows are hidden by the default, so the page can say so
	hidden := 0
	if f.Ign == "" {
		hc, ha := func() ([]string, []any) { g := f; g.Ign = "only"; return g.where(true) }()
		a.db.QueryRow(`SELECT COUNT(*) FROM transactions t WHERE `+strings.Join(hc, " AND "), ha...).Scan(&hidden)
	}
	showIgnoredURL := f.url(func(g *txnFilter) { g.Ign = "show" })
	first, last := (f.Page-1)*txnPageSize+1, (f.Page-1)*txnPageSize+len(txns)
	if total == 0 {
		first = 0
	}
	a.tpl.ExecuteTemplate(w, "index.html", map[string]any{
		"Txns": txns, "Cats": allCategories(), "Accounts": accts, "Groups": groups, "Sorts": sorts, "Hows": hows,
		"F": f, "SelAcct": selAcct, "SelCat": selCat, "Chips": chips, "Active": active, "NActive": len(active),
		"Total": total, "OutSum": out, "InSum": in, "Page": f.Page, "Pages": pages, "First": first, "Last": last,
		"PrevURL": prevURL, "NextURL": nextURL, "ClearURL": "/transactions", "HiddenIgnored": hidden, "ShowIgnoredURL": showIgnoredURL,
		"PlaidReady": a.pc() != nil, "Flash": q.Get("msg"),
	})
}

// fmtMoney formats a dollar amount with thousands separators: 13475.2 -> "13,475.20".
func fmtMoney(f float64) string {
	s := strconv.FormatFloat(f, 'f', 2, 64)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	out := b.String() + "." + frac
	if neg {
		out = "-" + out
	}
	return out
}
