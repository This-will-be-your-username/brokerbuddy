package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
)

// Reprocessing re-reads every stored transaction with the current parsing and categorisation
// logic: footer text from older imports is removed, then everything you have not set by hand is
// classified again. A preview shows what would change without writing anything.
// Transactions you confirmed yourself, and their ids, dates and amounts, are never touched.

type upd struct {
	id      string
	name    string
	oldCat  string
	oldConf int
	res     result
}

type recatPlan struct {
	ups     []upd
	Changed int // category differs
	Total   int // transactions examined (not set by hand)
}

func planRecategorize(db *sql.DB) (*recatPlan, error) {
	cl, err := newClassifier(db)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT plaid_txn_id, name, merchant, plaid_category, amount, category, confidence, reimb_override FROM transactions WHERE manual=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	p := &recatPlan{}
	for rows.Next() {
		var id, n, m, pc, oldCat string
		var amt float64
		var oldConf, over int
		if err := rows.Scan(&id, &n, &m, &pc, &amt, &oldCat, &oldConf, &over); err != nil {
			return nil, err
		}
		r := cl.classifyFullWith(n, m, pc, amt, over)
		if r.Cat != oldCat {
			p.Changed++
		}
		p.ups = append(p.ups, upd{id, n, oldCat, oldConf, r})
	}
	p.Total = len(p.ups)
	return p, rows.Err()
}

type levelCounts struct {
	Unknown, Low, Medium, High int
}

func (l *levelCounts) add(conf int, cat string) {
	switch confLevel(conf, false, cat) {
	case "unknown":
		l.Unknown++
	case "low":
		l.Low++
	case "medium":
		l.Medium++
	default:
		l.High++
	}
}

type changeSample struct {
	Name     string `json:"name"`
	From     string `json:"from"`
	To       string `json:"to"`
	FromConf int    `json:"fromConf"`
	ToConf   int    `json:"toConf"`
}

type reprocessReport struct {
	Applied      bool           `json:"applied"`
	NamesCleaned int            `json:"namesCleaned"`
	Examined     int            `json:"examined"`
	Changed      int            `json:"changed"`
	Before       levelCounts    `json:"before"`
	After        levelCounts    `json:"after"`
	Samples      []changeSample `json:"samples"`
}

// reprocess previews (apply=false) or performs (apply=true) the work.
func reprocess(db *sql.DB, apply bool) (*reprocessReport, error) {
	rep := &reprocessReport{Applied: apply}
	if apply {
		n, err := cleanStoredNames(db)
		if err != nil {
			return nil, err
		}
		rep.NamesCleaned = n
	} else {
		db.QueryRow(`SELECT COUNT(*) FROM transactions WHERE name LIKE '%opyright%' OR name LIKE '%©%'`).Scan(&rep.NamesCleaned)
	}
	plan, err := planRecategorize(db)
	if err != nil {
		return nil, err
	}
	rep.Examined, rep.Changed = plan.Total, plan.Changed
	var fixes, others []changeSample
	for _, u := range plan.ups {
		rep.Before.add(u.oldConf, u.oldCat)
		rep.After.add(u.res.Conf, u.res.Cat)
		if u.res.Cat == u.oldCat {
			continue
		}
		s := changeSample{u.name, u.oldCat, u.res.Cat, u.oldConf, u.res.Conf}
		if u.oldCat == "Uncategorized" {
			fixes = append(fixes, s)
		} else {
			others = append(others, s)
		}
	}
	sort.SliceStable(fixes, func(i, j int) bool { return fixes[i].ToConf > fixes[j].ToConf })
	rep.Samples = append(fixes, others...)
	if len(rep.Samples) > 15 {
		rep.Samples = rep.Samples[:15]
	}
	if apply {
		if _, err := recategorize(db); err != nil {
			return nil, err
		}
	}
	return rep, nil
}

// POST /api/reprocess  apply=1 to write, otherwise preview only
func (a *app) reprocessAPI(w http.ResponseWriter, r *http.Request) {
	rep, err := reprocess(a.db, r.FormValue("apply") == "1")
	if err != nil {
		fail(w, fmt.Errorf("reprocess: %w", err), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rep)
}
