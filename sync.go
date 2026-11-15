package main

import "errors"

var (
	errNoPlaid = errors.New("Plaid is not configured (set PLAID_CLIENT_ID and PLAID_SECRET)")
	errBad     = errors.New("bad request")
)

// syncItemByPlaidID pulls new/changed/removed transactions via cursor-based /transactions/sync
// and stores them atomically, advancing the cursor only on commit.
func (a *app) syncItemByPlaidID(plaidItemID string) error {
	if a.pc() == nil {
		return errNoPlaid
	}
	var id int
	var enc, cursor string
	if err := a.db.QueryRow(`SELECT id, access_token, cursor FROM items WHERE plaid_item_id=?`, plaidItemID).
		Scan(&id, &enc, &cursor); err != nil {
		return err
	}
	access, err := a.vault.open(enc)
	if err != nil {
		return err
	}
	cl, err := newClassifier(a.db)
	if err != nil {
		return err
	}

	// Collect all pages first so a mid-way failure doesn't leave a half-applied state.
	var all []syncResult
	for {
		res, err := a.pc().sync(access, cursor)
		if err != nil {
			return err
		}
		all = append(all, res)
		cursor = res.NextCursor
		if !res.HasMore {
			break
		}
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, res := range all {
		for _, ac := range res.Accounts {
			if _, err := tx.Exec(`INSERT INTO accounts(plaid_account_id,item_id,name,mask,type,is_card) VALUES(?,?,?,?,?,?)
				ON CONFLICT(plaid_account_id) DO UPDATE SET name=excluded.name, mask=excluded.mask`,
				ac.ID, id, ac.Name, ac.Mask, ac.Type, boolInt(ac.Type == "credit")); err != nil { // Plaid says which accounts are credit cards; your own flag wins afterwards
				return err
			}
		}
		for _, t := range append(res.Added, res.Modified...) {
			res := cl.classifyFull(t.Name, t.MerchantName, t.PFC.Primary, t.Amount)
			// Manual overrides survive re-syncs.
			if _, err := tx.Exec(`INSERT INTO transactions(plaid_txn_id,account_id,date,name,merchant,amount,plaid_category,category,cat_source,confidence,reimb)
				VALUES(?,?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(plaid_txn_id) DO UPDATE SET date=excluded.date, name=excluded.name, merchant=excluded.merchant,
					amount=excluded.amount, plaid_category=excluded.plaid_category,
					category=CASE WHEN transactions.manual=1 THEN transactions.category ELSE excluded.category END,
					cat_source=CASE WHEN transactions.manual=1 THEN transactions.cat_source ELSE excluded.cat_source END,
					confidence=CASE WHEN transactions.manual=1 THEN transactions.confidence ELSE excluded.confidence END,
					reimb=CASE WHEN transactions.manual=1 OR transactions.reimb_override<>0 THEN transactions.reimb ELSE excluded.reimb END`,
				t.ID, t.AccountID, t.Date, t.Name, t.MerchantName, t.Amount, t.PFC.Primary, res.Cat, res.Src, res.Conf, boolInt(res.Reimb)); err != nil {
				return err
			}
		}
		for _, rm := range res.Removed {
			if _, err := tx.Exec(`DELETE FROM transactions WHERE plaid_txn_id=?`, rm.ID); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`UPDATE items SET cursor=? WHERE id=?`, cursor, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = recomputeIgnored(a.db)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
