package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Minimal Plaid REST client (only the endpoints we need).
type plaidClient struct {
	base, id, secret string
	http             *http.Client
}

func newPlaid(env, id, secret string) *plaidClient {
	return &plaidClient{
		base: "https://" + env + ".plaid.com", id: id, secret: secret,
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

func (p *plaidClient) call(path string, req map[string]any, out any) error {
	req["client_id"], req["secret"] = p.id, p.secret
	b, _ := json.Marshal(req)
	resp, err := p.http.Post(p.base+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != 200 {
		var e struct {
			Code    string `json:"error_code"`
			Message string `json:"error_message"`
		}
		json.Unmarshal(body, &e)
		return fmt.Errorf("plaid %s: %s (%s)", path, e.Message, e.Code)
	}
	return json.Unmarshal(body, out)
}

func (p *plaidClient) linkToken() (string, error) {
	var r struct {
		LinkToken string `json:"link_token"`
	}
	err := p.call("/link/token/create", map[string]any{
		"client_name":   "BrokerBuddy",
		"language":      "en",
		"country_codes": []string{"US"},
		"products":      []string{"transactions"},
		"user":          map[string]any{"client_user_id": "local-user"},
	}, &r)
	return r.LinkToken, err
}

type exchanged struct {
	AccessToken string `json:"access_token"`
	ItemID      string `json:"item_id"`
}

func (p *plaidClient) exchange(public string) (exchanged, error) {
	var r exchanged
	err := p.call("/item/public_token/exchange", map[string]any{"public_token": public}, &r)
	return r, err
}

type pAccount struct {
	ID   string `json:"account_id"`
	Name string `json:"name"`
	Mask string `json:"mask"`
	Type string `json:"type"`
}

type pTxn struct {
	ID           string  `json:"transaction_id"`
	AccountID    string  `json:"account_id"`
	Date         string  `json:"date"`
	Name         string  `json:"name"`
	MerchantName string  `json:"merchant_name"`
	Amount       float64 `json:"amount"`
	PFC          struct {
		Primary string `json:"primary"`
	} `json:"personal_finance_category"`
}

type syncResult struct {
	Added    []pTxn `json:"added"`
	Modified []pTxn `json:"modified"`
	Removed  []struct {
		ID string `json:"transaction_id"`
	} `json:"removed"`
	Accounts   []pAccount `json:"accounts"`
	NextCursor string     `json:"next_cursor"`
	HasMore    bool       `json:"has_more"`
}

func (p *plaidClient) sync(access, cursor string) (syncResult, error) {
	var r syncResult
	req := map[string]any{"access_token": access, "count": 500}
	if cursor != "" {
		req["cursor"] = cursor
	}
	err := p.call("/transactions/sync", req, &r)
	return r, err
}

func (p *plaidClient) institution(access string) string {
	var item struct {
		Item struct {
			InstitutionID string `json:"institution_id"`
		} `json:"item"`
	}
	if p.call("/item/get", map[string]any{"access_token": access}, &item) != nil || item.Item.InstitutionID == "" {
		return "Bank"
	}
	var inst struct {
		Institution struct {
			Name string `json:"name"`
		} `json:"institution"`
	}
	if p.call("/institutions/get_by_id", map[string]any{
		"institution_id": item.Item.InstitutionID, "country_codes": []string{"US"},
	}, &inst) != nil {
		return "Bank"
	}
	return inst.Institution.Name
}

func (p *plaidClient) remove(access string) error {
	var r map[string]any
	return p.call("/item/remove", map[string]any{"access_token": access}, &r)
}
