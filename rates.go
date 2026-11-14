package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mortgage rates come from the Consumer Data Right "Product Reference Data" APIs. Every
// Australian bank, credit union and building society that is a CDR data holder must publish its
// current home-loan products and rates there, for free and without authentication. The list of
// data holders comes from the CDR Register. No personal data is sent: these are public GETs.
//
// Mortgage BROKERS are not data holders and publish no feed, so they cannot be listed. Brokers
// place loans with these same lenders, so the lender rates here are what they are working from.

const cdrRegister = "https://api.cdr.gov.au/cdr-register/v1/banking/data-holders/brands/summary"

var rateHTTP = &http.Client{Timeout: 25 * time.Second}

func cdrGet(u string, version string, out any) error {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-v", version)
	req.Header.Set("x-min-v", "1") // let each bank answer with the newest version it supports
	req.Header.Set("User-Agent", "BrokerBuddy/1.0 (personal budgeting tool)")
	resp, err := rateHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

type rateRow struct {
	Brand, ProductID, Product, Type string
	Rate, Comparison                float64
	FixedMonths                     int
	Repayment, Purpose, URL         string
	LVRMin, LVRMax                  *float64
	Special                         bool // staff / veteran / green-loan style products not open to everyone
}

var isoDuration = regexp.MustCompile(`^P(?:(\d+)Y)?(?:(\d+)M)?`)

func fixedMonths(s string) int {
	m := isoDuration.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(s)))
	if m == nil {
		return 0
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	return y*12 + mo
}

func numAny(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

type cdrLendingRate struct {
	Type            string `json:"lendingRateType"`
	Rate            any    `json:"rate"`
	Comparison      any    `json:"comparisonRate"`
	Repayment       string `json:"repaymentType"`
	Purpose         string `json:"loanPurpose"`
	Info            string `json:"additionalInfo"`
	InfoURI         string `json:"additionalInfoUri"`
	AdditionalValue string `json:"additionalValue"`
	Tiers           []struct {
		Unit string `json:"unitOfMeasure"`
		Name string `json:"name"`
		Min  any    `json:"minimumValue"`
		Max  any    `json:"maximumValue"`
	} `json:"tiers"`
	Conditions []struct {
		Type  string `json:"rateApplicabilityType"`
		Value string `json:"additionalValue"`
	} `json:"applicabilityConditions"`
}

var specialRe = regexp.MustCompile(`(?i)\b(staff|employees?|veterans?|defence|firefighters?|fire service|police|nurses?|teachers?|green|sustainab\w*|clean energy|eco|solar|energy[- ]efficient|bridging|construction|vacant land|equity access|guarantor|seniors?|retirement|reverse)\b`)
var fixedNameRe = regexp.MustCompile(`(?i)\bfixed\b`)
var yearsInName = regexp.MustCompile(`(?i)(\d+)\s*(?:yr|year)`)

// isSpecialProduct flags loans only certain people can get; their rates would mislead a general estimate.
func isSpecialProduct(name, desc string, eligibility []string) bool {
	if specialRe.MatchString(name) || specialRe.MatchString(desc) {
		return true
	}
	for _, e := range eligibility {
		if specialRe.MatchString(e) {
			return true
		}
	}
	return false
}

// parseProductRates turns one CDR product detail into rate rows (variable and fixed only).
func parseProductRates(brand, productID, name, appURI string, special bool, lrs []cdrLendingRate) []rateRow {
	var out []rateRow
	for _, lr := range lrs {
		t := strings.ToUpper(lr.Type)
		if t != "VARIABLE" && t != "FIXED" {
			continue // discounts, introductory offers, penalties etc. are not standalone rates
		}
		r, ok := numAny(lr.Rate)
		if !ok {
			continue
		}
		r *= 100
		if r < 1 || r > 20 {
			continue
		}
		monthsFromName := 0
		if t == "VARIABLE" && fixedNameRe.MatchString(name) { // some lenders label fixed loans as variable
			t = "FIXED"
			if m := yearsInName.FindStringSubmatch(name); m != nil {
				y, _ := strconv.Atoi(m[1])
				monthsFromName = y * 12
			}
		}
		row := rateRow{Special: special, Brand: brand, ProductID: productID, Product: name, Type: t, Rate: r,
			Repayment: lr.Repayment, Purpose: lr.Purpose, URL: appURI}
		if c, ok := numAny(lr.Comparison); ok && c*100 >= 1 && c*100 <= 25 {
			row.Comparison = c * 100
		}
		if lr.InfoURI != "" {
			row.URL = lr.InfoURI
		}
		if t == "FIXED" {
			row.FixedMonths = fixedMonths(lr.AdditionalValue)
			if row.FixedMonths == 0 {
				row.FixedMonths = monthsFromName
			}
			if row.FixedMonths == 0 {
				for _, c := range lr.Conditions {
					if m := fixedMonths(c.Value); m > 0 {
						row.FixedMonths = m
					}
				}
			}
		}
		for _, tier := range lr.Tiers {
			if strings.EqualFold(tier.Unit, "PERCENT") || strings.Contains(strings.ToUpper(tier.Name), "LVR") {
				if v, ok := numAny(tier.Min); ok {
					row.LVRMin = &v
				}
				if v, ok := numAny(tier.Max); ok {
					row.LVRMax = &v
				}
			}
		}
		out = append(out, row)
	}
	return out
}

type brandRef struct{ Name, Base string }

func fetchBrands() ([]brandRef, error) {
	var reg struct {
		Data []struct {
			Name string `json:"brandName"`
			Pub  string `json:"publicBaseUri"`
			Prod string `json:"productBaseUri"`
		} `json:"data"`
	}
	if err := cdrGet(cdrRegister, "2", &reg); err != nil {
		return nil, fmt.Errorf("CDR register: %w", err)
	}
	var out []brandRef
	for _, b := range reg.Data {
		base := b.Prod
		if base == "" {
			base = b.Pub
		}
		if base != "" {
			out = append(out, brandRef{b.Name, strings.TrimRight(base, "/")})
		}
	}
	return out, nil
}

type cdrProductList struct {
	Data struct {
		Products []struct {
			ID             string           `json:"productId"`
			Name           string           `json:"name"`
			Brand          string           `json:"brand"`
			BrandName      string           `json:"brandName"`
			ApplicationURI string           `json:"applicationUri"`
			LendingRates   []cdrLendingRate `json:"lendingRates"`
		} `json:"products"`
	} `json:"data"`
	Meta struct {
		TotalPages int `json:"totalPages"`
	} `json:"meta"`
}

func fetchBrandRates(b brandRef) ([]rateRow, error) {
	var rows []rateRow
	for page := 1; page <= 5; page++ {
		var list cdrProductList
		u := fmt.Sprintf("%s/cds-au/v1/banking/products?product-category=RESIDENTIAL_MORTGAGES&effective=CURRENT&page-size=500&page=%d", b.Base, page)
		if err := cdrGet(u, "10", &list); err != nil {
			return rows, err
		}
		for _, p := range list.Data.Products {
			brand := p.BrandName
			if brand == "" {
				brand = p.Brand
			}
			if brand == "" {
				brand = b.Name
			}
			lrs := p.LendingRates
			var desc string
			var elig []string
			if len(lrs) == 0 {
				var d struct {
					Data struct {
						Description string `json:"description"`
						Eligibility []struct {
							Info string `json:"additionalInfo"`
						} `json:"eligibility"`
						LendingRates []cdrLendingRate `json:"lendingRates"`
					} `json:"data"`
				}
				if err := cdrGet(b.Base+"/cds-au/v1/banking/products/"+url.PathEscape(p.ID), "10", &d); err != nil {
					continue // one broken product shouldn't hide the lender's others
				}
				lrs = d.Data.LendingRates
				desc = d.Data.Description
				for _, e := range d.Data.Eligibility {
					elig = append(elig, e.Info)
				}
				time.Sleep(100 * time.Millisecond)
			}
			rows = append(rows, parseProductRates(brand, p.ID, p.Name, p.ApplicationURI, isSpecialProduct(p.Name, desc, elig), lrs)...)
		}
		if page >= list.Meta.TotalPages {
			break
		}
	}
	return rows, nil
}

// ---- background refresh job ----

type rateJobState struct {
	sync.Mutex
	Running                   bool
	Total, Done, Failed, Rows int
	Started, Finished         time.Time
	Err                       string
}

var rateJob rateJobState

func (a *app) startRateRefresh() bool {
	rateJob.Lock()
	if rateJob.Running {
		rateJob.Unlock()
		return false
	}
	rateJob.Running, rateJob.Total, rateJob.Done, rateJob.Failed, rateJob.Rows, rateJob.Err = true, 0, 0, 0, 0, ""
	rateJob.Started = time.Now()
	rateJob.Unlock()
	go func() {
		defer func() { rateJob.Lock(); rateJob.Running, rateJob.Finished = false, time.Now(); rateJob.Unlock() }()
		brands, err := fetchBrands()
		if err != nil {
			rateJob.Lock()
			rateJob.Err = err.Error()
			rateJob.Unlock()
			log.Println("rates:", err)
			return
		}
		rateJob.Lock()
		rateJob.Total = len(brands)
		rateJob.Unlock()
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		now := time.Now().UTC().Format(time.RFC3339)
		for _, b := range brands {
			wg.Add(1)
			sem <- struct{}{}
			go func(b brandRef) {
				defer wg.Done()
				defer func() { <-sem }()
				rows, err := fetchBrandRates(b)
				rateJob.Lock()
				rateJob.Done++
				if err != nil && len(rows) == 0 {
					rateJob.Failed++
				}
				rateJob.Rows += len(rows)
				rateJob.Unlock()
				if len(rows) == 0 {
					return
				}
				a.storeRates(rows, now)
			}(b)
		}
		wg.Wait()
		// drop rows from earlier refreshes for lenders that were re-fetched this time
		a.db.Exec(`DELETE FROM mortgage_rates WHERE fetched_at < ?`, now)
	}()
	return true
}

func (a *app) storeRates(rows []rateRow, now string) {
	tx, err := a.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	brands := map[string]bool{}
	for _, r := range rows {
		brands[r.Brand] = true
	}
	for b := range brands {
		tx.Exec(`DELETE FROM mortgage_rates WHERE brand=?`, b)
	}
	for _, r := range rows {
		tx.Exec(`INSERT INTO mortgage_rates(brand,product_id,product,rate_type,rate,comparison,fixed_months,repayment,purpose,lvr_min,lvr_max,url,special,fetched_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, r.Brand, r.ProductID, r.Product, r.Type, r.Rate, r.Comparison, r.FixedMonths,
			r.Repayment, r.Purpose, r.LVRMin, r.LVRMax, r.URL, r.Special, now)
	}
	tx.Commit()
}

// ---- HTTP handlers ----

func (a *app) ratesRefresh(w http.ResponseWriter, r *http.Request) {
	started := a.startRateRefresh()
	json.NewEncoder(w).Encode(map[string]bool{"started": started})
}

func (a *app) ratesStatus(w http.ResponseWriter, r *http.Request) {
	rateJob.Lock()
	st := map[string]any{"running": rateJob.Running, "total": rateJob.Total, "done": rateJob.Done,
		"failed": rateJob.Failed, "rows": rateJob.Rows, "error": rateJob.Err}
	rateJob.Unlock()
	var n, lenders int
	var fetched *string
	a.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT brand), MAX(fetched_at) FROM mortgage_rates`).Scan(&n, &lenders, &fetched)
	st["cached"], st["lenders"] = n, lenders
	if fetched != nil {
		st["fetched"] = *fetched
	}
	json.NewEncoder(w).Encode(st)
}

func (a *app) ratesList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	where, args := []string{"1=1"}, []any{}
	if p := q.Get("purpose"); p != "" { // OWNER_OCCUPIED | INVESTMENT (unconstrained products fit both)
		where = append(where, "(purpose=? OR purpose='UNCONSTRAINED' OR purpose='')")
		args = append(args, p)
	}
	if t := q.Get("type"); t != "" {
		where = append(where, "rate_type=?")
		args = append(args, t)
	}
	if rp := q.Get("repayment"); rp != "" {
		where = append(where, "(repayment=? OR repayment='UNCONSTRAINED' OR repayment='')")
		args = append(args, rp)
	}
	showAll := q.Get("all") == "1"
	if !showAll {
		where = append(where, "special=0")
	}
	if s := strings.TrimSpace(q.Get("q")); s != "" {
		where = append(where, "(brand LIKE ? OR product LIKE ?)")
		args = append(args, "%"+s+"%", "%"+s+"%")
	}
	// An unusually low rate (more than 1.25 points under the median) is almost always a product with
	// special eligibility; hide those unless the user asks to see everything.
	if !showAll {
		var med float64
		a.db.QueryRow(`SELECT rate FROM mortgage_rates WHERE special=0 AND rate_type=? ORDER BY rate LIMIT 1 OFFSET (SELECT COUNT(*)/2 FROM mortgage_rates WHERE special=0 AND rate_type=?)`,
			firstNonEmpty(q.Get("type"), "VARIABLE"), firstNonEmpty(q.Get("type"), "VARIABLE")).Scan(&med)
		if med > 0 {
			where = append(where, "rate>=?")
			args = append(args, med-1.25)
		}
	}
	rows, err := a.db.Query(`SELECT DISTINCT brand, product, rate_type, rate, comparison, fixed_months, repayment, purpose, lvr_min, lvr_max, url
		FROM mortgage_rates WHERE `+strings.Join(where, " AND ")+` ORDER BY rate ASC LIMIT 500`, args...)
	if err != nil {
		fail(w, err, 500)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var brand, product, rt, rep, pur, u string
		var rate, cmp float64
		var fm int
		var lmin, lmax *float64
		rows.Scan(&brand, &product, &rt, &rate, &cmp, &fm, &rep, &pur, &lmin, &lmax, &u)
		out = append(out, map[string]any{"brand": brand, "product": product, "type": rt, "rate": rate, "comparison": cmp,
			"fixedMonths": fm, "repayment": rep, "purpose": pur, "lvrMin": lmin, "lvrMax": lmax, "url": u})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
