package main

import (
	"database/sql"
	"strings"
)

type rule struct{ Pattern, Category string }

func loadRules(db *sql.DB) ([]rule, error) {
	rows, err := db.Query(`SELECT pattern, category FROM rules ORDER BY id DESC`) // newest first
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rule
	for rows.Next() {
		var r rule
		if err := rows.Scan(&r.Pattern, &r.Category); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func loadCache(db *sql.DB) (map[string]cacheEntry, error) {
	rows, err := db.Query(`SELECT key, category, source, detail FROM merchant_cache`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]cacheEntry{}
	for rows.Next() {
		var k string
		var e cacheEntry
		if err := rows.Scan(&k, &e.Category, &e.Source, &e.Detail); err != nil {
			return nil, err
		}
		m[k] = e
	}
	return m, rows.Err()
}

// newClassifier snapshots rules + lookup cache. Must be called outside any open db transaction
// (the connection pool has a single connection).
func newClassifier(db *sql.DB) (*classifier, error) {
	r, err := loadRules(db)
	if err != nil {
		return nil, err
	}
	c, err := loadCache(db)
	if err != nil {
		return nil, err
	}
	l, err := loadLearner(db)
	if err != nil {
		return nil, err
	}
	h, err := loadHistory(db)
	if err != nil {
		return nil, err
	}
	return &classifier{rules: r, cache: c, learn: l, hist: h, reimb: loadReimbCfg(db)}, nil
}

// loadHistory collects merchants whose category is trustworthy, to guess for look-alikes.
// Earlier guesses (fuzzy, history, cue, hint) are excluded so errors can't feed on themselves.
func loadHistory(db *sql.DB) (*history, error) {
	rows, err := db.Query(`SELECT name, merchant, category, CASE WHEN manual=1 THEN 100 ELSE confidence END FROM transactions
		WHERE category<>'Uncategorized' AND (manual=1 OR cat_source IN ('rule','builtin','learned','lookup'))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	h := &history{}
	seen := map[string]int{}
	for rows.Next() {
		var n, m, c string
		var conf int
		if err := rows.Scan(&n, &m, &c, &conf); err != nil {
			return nil, err
		}
		toks := descriptorTokens(n + " " + m)
		if len(toks) == 0 {
			continue
		}
		key := strings.Join(toks, " ")
		id := key + "|" + c
		if i, ok := seen[id]; ok {
			h.ex[i].conf = max(h.ex[i].conf, conf)
			continue
		}
		seen[id] = len(h.ex)
		h.ex = append(h.ex, histEx{toks, key, c, conf})
	}
	return h, rows.Err()
}

// recategorizeAll reapplies the classifier to every transaction the user hasn't set by hand.
func recategorizeAll(db *sql.DB) error {
	_, err := recategorize(db)
	return err
}

// recategorize returns how many transactions ended up with a different category.
func recategorize(db *sql.DB) (int, error) {
	cl, err := newClassifier(db)
	if err != nil {
		return 0, err
	}
	rows, err := db.Query(`SELECT plaid_txn_id, name, merchant, plaid_category, amount, category, confidence, reimb_override FROM transactions WHERE manual=0`)
	if err != nil {
		return 0, err
	}
	type upd struct {
		id  string
		res result
	}
	var ups []upd
	changed := 0
	for rows.Next() {
		var id, n, m, p, oldCat string
		var amt float64
		var oldConf, over int
		if err := rows.Scan(&id, &n, &m, &p, &amt, &oldCat, &oldConf, &over); err != nil {
			rows.Close()
			return 0, err
		}
		r := cl.classifyFullWith(n, m, p, amt, over)
		if r.Cat != oldCat {
			changed++
		}
		ups = append(ups, upd{id, r})
	}
	rows.Close()
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, u := range ups {
		if _, err := tx.Exec(`UPDATE transactions SET category=?, cat_source=?, confidence=?, reimb=? WHERE plaid_txn_id=?`, u.res.Cat, u.res.Src, u.res.Conf, boolInt(u.res.Reimb), u.id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if _, err := recomputeIgnored(db); err != nil { // categories feed the card-repayment rule
		return changed, err
	}
	return changed, nil
}

// applyToSameMerchant looks through the transaction history for other transactions from the same
// merchant as the edited one (same descriptor words once store numbers, suburbs and states are
// stripped) and gives the ones you haven't set yourself the new category. It returns how many
// changed. Credits and debits are kept apart: a debit never becomes income.
func (a *app) applyToSameMerchant(id, cat string) (int, error) {
	var name, merchant string
	var amount float64
	if err := a.db.QueryRow(`SELECT name, merchant, amount FROM transactions WHERE plaid_txn_id=?`, id).Scan(&name, &merchant, &amount); err != nil {
		return 0, err
	}
	key := strings.Join(descriptorTokens(name+" "+merchant), " ")
	if key == "" {
		return 0, nil
	}
	rows, err := a.db.Query(`SELECT plaid_txn_id, name, merchant, amount, category, confidence FROM transactions WHERE manual=0 AND plaid_txn_id<>?`, id)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var tid, n, m, c string
		var amt float64
		var conf int
		if err := rows.Scan(&tid, &n, &m, &amt, &c, &conf); err != nil {
			rows.Close()
			return 0, err
		}
		if (c == cat && conf >= 90) || (isIncomeCat(cat) && amt > 0) {
			continue
		}
		if strings.Join(descriptorTokens(n+" "+m), " ") == key {
			ids = append(ids, tid)
		}
	}
	rows.Close()
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, tid := range ids {
		if _, err := tx.Exec(`UPDATE transactions SET category=?, cat_source='learned', confidence=90 WHERE plaid_txn_id=?`, cat, tid); err != nil {
			return 0, err
		}
	}
	return len(ids), tx.Commit()
}
