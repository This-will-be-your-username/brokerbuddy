package main

import (
	"encoding/json"
	"net/url"
	"testing"
)

func seedStale(t *testing.T) *app {
	a := testApp(t)
	withAccount(a)
	// stored by an older version: footer text glued on, wrong/unknown category
	addTxn(t, a, "s1", "2026-03-01", "WOOLWORTHS 1234 - Visa Purchase - Receipt 1 Copyright © 2026 X Banking Corporation", 40, "Uncategorized", 0, false, "")
	addTxn(t, a, "s2", "2026-03-02", "SOME BUTCHER SUBURB", 30, "Shopping & Clothing", 40, false, "cue")
	addTxn(t, a, "m1", "2026-03-03", "NETFLIX.COM", 20, "Groceries", 100, true, "manual") // user's choice, however odd
	return a
}

func TestReprocessPreviewChangesNothing(t *testing.T) {
	a := seedStale(t)
	rep, err := reprocess(a.db, false)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Applied || rep.Changed < 2 || rep.NamesCleaned != 1 || rep.Examined != 2 {
		t.Errorf("%+v", rep)
	}
	var cat, name string
	a.db.QueryRow(`SELECT category, name FROM transactions WHERE plaid_txn_id='s1'`).Scan(&cat, &name)
	if cat != "Uncategorized" || len(name) < 60 {
		t.Errorf("preview must not write: %s %q", cat, name)
	}
}

func TestReprocessAppliesNewLogicAndKeepsManualChoices(t *testing.T) {
	a := seedStale(t)
	c := newClient(t, a)
	r := c.post("/api/reprocess", url.Values{"apply": {"1"}})
	var rep reprocessReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		t.Fatal(err)
	}
	if !rep.Applied || rep.NamesCleaned != 1 || rep.After.Unknown >= rep.Before.Unknown {
		t.Errorf("%+v", rep)
	}
	get := func(id string) (cat, name string) {
		a.db.QueryRow(`SELECT category, name FROM transactions WHERE plaid_txn_id=?`, id).Scan(&cat, &name)
		return
	}
	if cat, name := get("s1"); cat != "Groceries" || name != "WOOLWORTHS 1234 - Visa Purchase - Receipt 1" {
		t.Errorf("s1: %s %q", cat, name)
	}
	if cat, _ := get("s2"); cat == "Shopping & Clothing" {
		t.Errorf("s2 should be reclassified: %s", cat)
	}
	if cat, _ := get("m1"); cat != "Groceries" {
		t.Errorf("manual choice must survive: %s", cat)
	}
	again, _ := reprocess(a.db, true)
	if again.Changed != 0 || again.NamesCleaned != 0 {
		t.Errorf("idempotent: %+v", again)
	}
}
