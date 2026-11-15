package main

import (
	"regexp"
	"strings"
)

// Every categorisation carries a confidence score from 0 to 100 so that doubtful ones are easy to
// find and fix. The classifier always makes an attempt, working down this ladder until something
// matches:
//
//	you set it by hand ............................ 100  confirmed
//	your own rule ................................. 95
//	merchant you categorised before (same name) ... 90
//	built-in merchant list ........................ 65-90 (specific names score higher than words like "cafe")
//	merchant you categorised before (same brand) .. 60
//	the bank's own category ....................... 50-55
//	online lookup ................................. 50
//	typo of a known brand ......................... 52
//	similar to other transactions in your history . 30-55 (70 if the name is identical)
//	wording cues (fee, cash, credit with no clue) . 20-45
//	nothing found ................................. 0   -> "Uncategorized", flagged for review

type result struct {
	Cat, Src string
	Conf     int
	Reimb    bool // a repayment from another person (offsets spending)
}

// confLevel buckets a score for display and filtering.
func confLevel(conf int, manual bool, cat string) string {
	switch {
	case manual || conf >= 100:
		return "confirmed"
	case cat == "Uncategorized" || conf <= 0:
		return "unknown"
	case conf >= 80:
		return "high"
	case conf >= 50:
		return "medium"
	}
	return "low"
}

// srcWhy explains a source in plain words (shown as a tooltip).
func srcWhy(src string, manual bool) string {
	if manual {
		return "You set or confirmed this category."
	}
	switch src {
	case "rule":
		return "Matched one of your rules."
	case "learned":
		return "Matched a merchant you categorised before."
	case "builtin":
		return "Matched a known merchant or keyword."
	case "lookup":
		return "Guessed from an online lookup of the business name."
	case "hint":
		return "Based on the category your bank supplied."
	case "fuzzy":
		return "The name is close to a known merchant (possible typo)."
	case "history":
		return "Similar to other transactions in your history."
	case "cue":
		return "A weak guess from the wording of the description."
	case "reimb":
		return "Looks like a repayment from another person, so it offsets spending instead of counting as income. Pick the category it relates to if it is not right."
	}
	return "No match found. Set a category to teach the app."
}

// Phrases that are ordinary words rather than a business name: a match is real but less certain.
var genericPhrases = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Split("cafe|coffee|restaurant|kebab|sushi|thai|pizza|bakery|bar and grill|pub|tavern|bistro|takeaway|burger|noodle|ramen|dumpling|gelato|donut|"+
		"supermarket|grocer|butcher|green grocer|fruit and veg|farmers market|petrol|fuel|service station|taxi|parking|toll|mechanic|car wash|rego|bus|train|"+
		"electricity|gas bill|water bill|internet|mobile plan|insurance|assurance|ambulance|medical|doctor|dental|dentist|physio|chemist|pharmacy|gym|hospital|"+
		"hardware|council|plumber|electrician|handyman|lawn|garden|childcare|child care|day care|daycare|kindergarten|school|university|tuition|"+
		"subscription|membership|cinema|theatre|museum|zoo|concert|festival|golf|bowling|hairdresser|barber|salon|beauty|nails|massage|waxing|tanning|"+
		"motel|resort|hostel|cruise|airport|vet|veterinary|pet shop|charity|donation|gift|florist|flowers|liquor|vape|tobacco|casino|lottery|poker|betting|wagering|bingo|"+
		"rent|rental|real estate|landlord|tenant|loan|mortgage|interest|dividend|investment|savings|transfer|salary|wages|wage|employer|payroll|tax payment|atm|overdrawn|bonus interest|refund|reimbursement|cashback|amex|credit card", "|") {
		m[norm(w)] = true
	}
	return m
}()

// Names that are also everyday words: a match is plausible but not certain.
var ambiguousPhrases = map[string]bool{"shell": true, "target": true, "stan": true, "ola": true, "tab": true, "dell": true, "binge": true,
	"ando": true, "belong": true, "steam": true, "stake": true, "catch": true, "mate": true, "bunnings": false}

func phraseConf(p string, glued bool) int {
	if c, ok := lowConfPhrases[p]; ok {
		return c
	}
	c := 85
	switch {
	case genericPhrases[p]:
		c = 66
	case ambiguousPhrases[p]:
		c = 70
	case strings.Contains(p, " ") || len(p) >= 6:
		c = 90
	case len(p) <= 2:
		c = 72 // two letters match by accident more easily
	case len(p) == 3:
		c = 80
	}
	if glued { // matched inside a longer unbroken string, e.g. "WOOLWORTHS1234"
		c -= 5
	}
	return c
}

// brandWords are the single-word, brand-like dictionary entries used to forgive typos in names.
var brandWords = func() map[string]string {
	m := map[string]string{}
	for _, e := range dict {
		if !strings.Contains(e.pat, " ") && len(e.pat) >= 6 && !genericPhrases[e.pat] && !isIncomeCat(e.cat) {
			m[e.pat] = e.cat
		}
	}
	return m
}()

// fuzzyBrand finds a known brand within a typo or truncation of one of the description's words.
func fuzzyBrand(toks []string, amount float64) (string, bool) {
	bestCat, bestD := "", 99
	for _, t := range toks {
		if len(t) < 6 {
			continue
		}
		for w, cat := range brandWords {
			if w == t || w[0] != t[0] || abs(len(w)-len(t)) > 2 {
				continue
			}
			if !tokEq(t, w) {
				continue
			}
			if d := levenshtein(t, w); d < bestD {
				bestCat, bestD = cat, d
			}
		}
	}
	return bestCat, bestCat != ""
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// ---- similarity to the user's own history ------------------------------------------------------

type histEx struct {
	toks []string
	key  string
	cat  string
	conf int
}

// history holds merchants whose category is trustworthy (confirmed, rule, dictionary, lookup or
// learned): guesses are never used as evidence for further guesses.
type history struct{ ex []histEx }

func tokWeights(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = 1 / float64(int(1)<<uint(min(i, 3)))
	}
	return w
}

// similarity is a weighted Dice score over descriptor words (earlier words, the brand, count more).
func similarity(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	wa, wb := tokWeights(len(a)), tokWeights(len(b))
	used := make([]bool, len(b))
	var m, sa, sb float64
	for _, w := range wa {
		sa += w
	}
	for _, w := range wb {
		sb += w
	}
	for i, x := range a {
		for j, y := range b {
			if !used[j] && tokEq(x, y) {
				used[j] = true
				m += min(wa[i], wb[j])
				break
			}
		}
	}
	return 2 * m / (sa + sb)
}

func (h *history) guess(toks []string, amount float64) (string, int, bool) {
	if h == nil || len(toks) == 0 {
		return "", 0, false
	}
	key := strings.Join(toks, " ")
	bestCat, bestSim, bestConf, exact := "", 0.0, 0, false
	for _, e := range h.ex {
		if isIncomeCat(e.cat) && amount > 0 { // a debit is never income
			continue
		}
		if e.key == key {
			if !exact || e.conf > bestConf {
				bestCat, bestConf, exact = e.cat, e.conf, true
			}
			continue
		}
		if exact {
			continue
		}
		if s := similarity(toks, e.toks); s > bestSim {
			bestCat, bestSim, bestConf = e.cat, s, e.conf
		}
	}
	switch {
	case exact:
		return bestCat, min(70, bestConf), true
	case bestSim >= 0.7:
		// A look-alike is only a guess: keep it in the "low" band (under 50) so it is easy to review.
		return bestCat, min(45, 25+int(20*bestSim)), true
	}
	return "", 0, false
}

// ---- wording cues -----------------------------------------------------------------------------

var (
	reCueFee  = regexp.MustCompile(`\b(fee|fees|charge|charges)\b`)
	reCueCash = regexp.MustCompile(`\b(cash|withdrawal)\b`)
	reCueThx  = regexp.MustCompile(`\bthank you\b`)
)

func cueGuess(text string, amount float64) (string, int, bool) {
	switch {
	case reCueThx.MatchString(text):
		return "Transfers", 40, true
	case amount > 0 && reCueFee.MatchString(text):
		return "Fees & Dishonours", 40, true
	case amount > 0 && reCueCash.MatchString(text):
		return "Cash Withdrawals", 45, true
	case amount < 0:
		return "Other Income", 20, true // money in with no other clue
	}
	return "", 0, false
}

// ---- the classifier ---------------------------------------------------------------------------

// classifyFull always attempts a category and says how sure it is.
func (c *classifier) classifyFull(name, merchant, hint string, amount float64) result {
	return c.classifyFullWith(name, merchant, hint, amount, 0)
}

// classifyFullWith is classifyFull plus the user's repayment override (1 yes, -1 no, 0 automatic):
// a small credit from another person is flagged and assigned the spending category it offsets.
func (c *classifier) classifyFullWith(name, merchant, hint string, amount float64, reimbOverride int) result {
	r := c.classifyBase(name, merchant, hint, amount)
	if amount >= 0 || reimbOverride == -1 || (!c.reimb.enabled && reimbOverride != 1) {
		return r
	}
	return c.reimbursement(name, merchant, amount, r, reimbOverride)
}

// classifyBase is the ordinary ladder, before repayments from others are considered.
func (c *classifier) classifyBase(name, merchant, hint string, amount float64) result {
	hay := strings.ToLower(name + " " + merchant)
	for _, r := range c.rules {
		if r.Pattern != "" && strings.Contains(hay, strings.ToLower(r.Pattern)) {
			return result{r.Category, "rule", 95, false}
		}
	}
	lcat, strong, lok := c.learn.match(name, merchant, amount)
	if lok && strong {
		return result{lcat, "learned", 90, false} // you categorised this merchant before: beats the built-in list
	}
	text := norm(cleanText(name + " " + merchant)) // how it was paid is not who was paid
	if amount > 0 && reAirTicket.MatchString(text) {
		return result{"Travel & Holidays", "builtin", 85, false} // airline ticket number
	}
	if amount > 0 && reTollRef.MatchString(text) {
		return result{"Transport & Fuel", "builtin", 85, false} // toll account reference, e.g. ETOLL4489362794
	}
	if cat, phrase, glued, ok := dictLookup(text, amount); ok {
		return result{cat, "builtin", phraseConf(phrase, glued), false}
	}
	if lok {
		return result{lcat, "learned", 60, false} // same brand as something you categorised, different details
	}
	if key := merchantKey(name + " " + merchant); key != "" {
		for _, k := range []string{key, strings.Fields(key)[0]} { // full key, then first word (drops suburb noise)
			if e, ok := c.cache[k]; ok && e.Category != "" {
				return result{e.Category, "lookup", 50, false}
			}
		}
	}
	if t, ok := strings.CutPrefix(hint, "csv:"); ok && strings.TrimSpace(t) != "" {
		return result{strings.TrimSpace(t), "hint", 55, false}
	}
	if cat, ok := pfcMap[hint]; ok && !(isIncomeCat(cat) && amount > 0) {
		return result{cat, "hint", 50, false}
	}
	toks := descriptorTokens(name + " " + merchant)
	if cat, ok := fuzzyBrand(toks, amount); ok {
		return result{cat, "fuzzy", 52, false}
	}
	if cat, ok := suffixGuess(toks); ok {
		return result{cat, "fuzzy", 50, false} // a run-together name ending in a business word, e.g. "primemeat"
	}
	if cat, conf, ok := c.hist.guess(toks, amount); ok {
		return result{cat, "history", conf, false}
	}
	if cat, conf, ok := cueGuess(text, amount); ok {
		return result{cat, "cue", conf, false}
	}
	return result{"Uncategorized", "", 0, false}
}

// classify keeps the simple (category, source) form.
func (c *classifier) classify(name, merchant, hint string, amount float64) (string, string) {
	r := c.classifyFull(name, merchant, hint, amount)
	return r.Cat, r.Src
}

// An airline name immediately followed by a long ticket number, e.g. "UNITED1621150119831".
var reAirTicket = regexp.MustCompile(`\b(united|delta|american|qantas|jetstar|virgin|emirates|qatar|etihad|singapore|cathay|lufthansa|british|airnz|air ?canada|fiji|rex|tigerair|scoot)\d{9,}\b`)

// A toll account reference: the operator's name run together with the account number.
var reTollRef = regexp.MustCompile(`\b(etoll|linkt|etag)\d{6,}\b`)

// Names that run a business word onto the end ("primemeat", "chargrillcafe"): the ending says
// what kind of business it is. Only a guess, so it scores in the medium band.
var suffixCats = []struct{ suf, cat string }{
	{"meats", "Groceries"}, {"meat", "Groceries"}, {"seafood", "Groceries"},
	{"bakery", "Dining & Takeaway"}, {"pizza", "Dining & Takeaway"}, {"burger", "Dining & Takeaway"}, {"kebab", "Dining & Takeaway"},
	{"sushi", "Dining & Takeaway"}, {"noodles", "Dining & Takeaway"}, {"coffee", "Dining & Takeaway"}, {"cafe", "Dining & Takeaway"},
	{"pharmacy", "Health & Medical"}, {"chemist", "Health & Medical"}, {"physio", "Health & Medical"}, {"dental", "Health & Medical"},
	{"cellars", "Alcohol & Tobacco"}, {"liquor", "Alcohol & Tobacco"},
}

func suffixGuess(toks []string) (string, bool) {
	for _, t := range toks {
		for _, s := range suffixCats {
			if len(t) >= len(s.suf)+5 && strings.HasSuffix(t, s.suf) {
				return s.cat, true
			}
		}
	}
	return "", false
}
