package main

import "database/sql"

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
	return &classifier{rules: r, cache: c, learn: l}, nil
}

// recategorizeAll reapplies the classifier to every transaction the user hasn't set by hand.
func recategorizeAll(db *sql.DB) error {
	cl, err := newClassifier(db)
	if err != nil {
		return err
	}
	rows, err := db.Query(`SELECT plaid_txn_id, name, merchant, plaid_category, amount FROM transactions WHERE manual=0`)
	if err != nil {
		return err
	}
	type upd struct{ id, cat, src string }
	var ups []upd
	for rows.Next() {
		var id, n, m, p string
		var amt float64
		if err := rows.Scan(&id, &n, &m, &p, &amt); err != nil {
			rows.Close()
			return err
		}
		cat, src := cl.classify(n, m, p, amt)
		ups = append(ups, upd{id, cat, src})
	}
	rows.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, u := range ups {
		if _, err := tx.Exec(`UPDATE transactions SET category=?, cat_source=? WHERE plaid_txn_id=?`, u.cat, u.src, u.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
