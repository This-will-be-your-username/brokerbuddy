package main

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS items (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	plaid_item_id TEXT UNIQUE NOT NULL,
	access_token TEXT NOT NULL,          -- AES-GCM encrypted
	institution TEXT NOT NULL,
	cursor TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS accounts (
	plaid_account_id TEXT PRIMARY KEY,
	item_id INTEGER REFERENCES items(id) ON DELETE CASCADE,  -- NULL = manual/CSV account
	name TEXT NOT NULL,
	mask TEXT NOT NULL DEFAULT '',
	type TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS transactions (
	plaid_txn_id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES accounts(plaid_account_id) ON DELETE CASCADE,
	date TEXT NOT NULL,
	name TEXT NOT NULL,
	merchant TEXT NOT NULL DEFAULT '',
	amount REAL NOT NULL,                -- Plaid sign: positive = money out
	plaid_category TEXT NOT NULL DEFAULT '',
	category TEXT NOT NULL DEFAULT 'Uncategorized',
	manual INTEGER NOT NULL DEFAULT 0,   -- 1 = user override, never auto-recategorized
	cat_source TEXT NOT NULL DEFAULT ''  -- rule | builtin | lookup | hint | '' (unmatched)
);
CREATE INDEX IF NOT EXISTS idx_txn_date ON transactions(date DESC);
CREATE TABLE IF NOT EXISTS csv_profiles (
	signature TEXT PRIMARY KEY,         -- hash of the CSV header row
	mapping TEXT NOT NULL,
	account_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS merchant_cache (
	key TEXT PRIMARY KEY,                -- cleaned merchant name
	category TEXT NOT NULL DEFAULT '',    -- empty = looked up, nothing found
	source TEXT NOT NULL DEFAULT '',
	detail TEXT NOT NULL DEFAULT '',
	looked_up_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS mortgage_rates (
	brand TEXT NOT NULL,
	product_id TEXT NOT NULL,
	product TEXT NOT NULL,
	rate_type TEXT NOT NULL,             -- VARIABLE | FIXED
	rate REAL NOT NULL,                  -- percent p.a., e.g. 6.19
	comparison REAL NOT NULL DEFAULT 0,
	fixed_months INTEGER NOT NULL DEFAULT 0,
	repayment TEXT NOT NULL DEFAULT '',
	purpose TEXT NOT NULL DEFAULT '',
	lvr_min REAL, lvr_max REAL,
	url TEXT NOT NULL DEFAULT '',
	special INTEGER NOT NULL DEFAULT 0,
	fetched_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_rates_rate ON mortgage_rates(rate);
CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL                  -- secrets are stored AES-GCM encrypted
);
CREATE TABLE IF NOT EXISTS rules (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	pattern TEXT NOT NULL,               -- case-insensitive substring of name/merchant
	category TEXT NOT NULL
);
`

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schema); err != nil {
		return nil, err
	}
	// Upgrade databases created before cat_source existed (error = column already there).
	db.Exec(`ALTER TABLE transactions ADD COLUMN cat_source TEXT NOT NULL DEFAULT ''`)
	return db, nil
}
