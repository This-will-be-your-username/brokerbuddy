package main

import (
	"database/sql"
	"strings"
)

// Learning from manual categorisations.
//
// Every transaction you categorise by hand becomes an example. New transactions are compared
// with those examples using the business-name words of the narrative:
//
//   - Store numbers, terminals, suburbs and states are stripped ("BUNNINGS 7788 PENRITH NSW" -> [bunnings]).
//   - Words match even with small typos or truncation ("ZORBA" ~ "ZORBAS", "WOOLWORTH" ~ "WOOLWORTHS").
//   - STRONG match: every word of the example appears in the new transaction. Beats the built-in
//     dictionary, because it says "you categorised this merchant like this".
//   - WEAK match: only the first word (the brand) matches, e.g. example "COLES EXPRESS" vs new "COLES".
//     Used only where the dictionary has no answer, and only if all your examples for that brand agree.

type example struct {
	toks  []string
	cat   string
	order int // later = more recent, wins ties
}

type learner struct{ ex []example }

var stateCodes = map[string]bool{"nsw": true, "vic": true, "qld": true, "wa": true, "sa": true, "tas": true, "act": true, "nt": true}

// descriptorTokens extracts the identifying words of a bank narrative.
func descriptorTokens(s string) []string {
	var kept []string
	for _, t := range strings.Fields(norm(s)) {
		if stateCodes[t] { // "... NEWTOWN NSW": state ends the descriptor; the word before it is the suburb
			if len(kept) >= 2 {
				kept = kept[:len(kept)-1]
			}
			break
		}
		if stopTokens[t] || len(t) < 3 {
			continue
		}
		if strings.ContainsAny(t, "0123456789") { // store / terminal / date number
			if len(kept) > 0 {
				break // everything after it is location noise
			}
			continue
		}
		kept = append(kept, t)
		if len(kept) == 4 {
			break
		}
	}
	return kept
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// tokEq: exact, or (for words of 5+ letters) a prefix/near-typo match.
func tokEq(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) < 5 || len(b) < 5 {
		return false
	}
	if strings.HasPrefix(a, b) || strings.HasPrefix(b, a) {
		return true
	}
	limit := 1
	if min(len(a), len(b)) >= 9 {
		limit = 2
	}
	return levenshtein(a, b) <= limit
}

func covered(ex, txn []string) bool {
	for _, e := range ex {
		found := false
		for _, t := range txn {
			if tokEq(e, t) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func loadLearner(db *sql.DB) (*learner, error) {
	rows, err := db.Query(`SELECT name, merchant, category FROM transactions WHERE manual=1 AND category<>'Uncategorized' ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	l := &learner{}
	seen := map[string]int{} // dedupe identical examples; keep the latest
	counter := 0
	for rows.Next() {
		var n, m, c string
		if err := rows.Scan(&n, &m, &c); err != nil {
			return nil, err
		}
		counter++
		toks := descriptorTokens(n + " " + m)
		if len(toks) == 0 {
			continue
		}
		key := strings.Join(toks, " ")
		if i, ok := seen[key]; ok {
			l.ex[i].cat, l.ex[i].order = c, counter
			continue
		}
		seen[key] = len(l.ex)
		l.ex = append(l.ex, example{toks, c, counter})
	}
	return l, rows.Err()
}

// match returns (category, strong, ok).
func (l *learner) match(name, merchant string, amount float64) (string, bool, bool) {
	if l == nil || len(l.ex) == 0 {
		return "", false, false
	}
	toks := descriptorTokens(name + " " + merchant)
	if len(toks) == 0 {
		return "", false, false
	}
	usable := func(e example) bool { return !(isIncomeCat(e.cat) && amount > 0) } // a debit can't be income

	var best *example
	for i := range l.ex {
		e := &l.ex[i]
		if usable(*e) && covered(e.toks, toks) {
			if best == nil || len(e.toks) > len(best.toks) || (len(e.toks) == len(best.toks) && e.order > best.order) {
				best = e
			}
		}
	}
	if best != nil {
		return best.cat, true, true
	}

	// Weak: same brand word; only if every example for that brand agrees.
	cat := ""
	for _, e := range l.ex {
		if !usable(e) || !tokEq(e.toks[0], toks[0]) {
			continue
		}
		if cat != "" && cat != e.cat {
			return "", false, false // conflicting examples: don't guess
		}
		cat = e.cat
	}
	return cat, false, cat != ""
}
