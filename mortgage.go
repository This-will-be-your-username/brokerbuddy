package main

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
)

// ---- affordability model (pure functions, unit-tested) -------------------

// pvAnnuity: the loan a monthly payment can service at an annual rate over a term.
func pvAnnuity(payment, annualPct float64, years int) float64 {
	n := float64(years * 12)
	if payment <= 0 || n <= 0 {
		return 0
	}
	r := annualPct / 100 / 12
	if r < 1e-9 {
		return payment * n
	}
	return payment * (1 - math.Pow(1+r, -n)) / r
}

// pmt: monthly principal-and-interest repayment.
func pmt(loan, annualPct float64, years int) float64 {
	n := float64(years * 12)
	if loan <= 0 || n <= 0 {
		return 0
	}
	r := annualPct / 100 / 12
	if r < 1e-9 {
		return loan / n
	}
	return loan * r / (1 - math.Pow(1+r, -n))
}

type baseLine struct {
	Category string  `json:"category"`
	Group    string  `json:"group"`
	Monthly  float64 `json:"monthly"`
}

type mortgageIn struct {
	Down         float64            `json:"down"`
	State        string             `json:"state"`
	Buyer        string             `json:"buyer"` // owner | fhb | investor
	TermYears    int                `json:"termYears"`
	RatePct      float64            `json:"ratePct"`
	BufferPct    float64            `json:"bufferPct"`
	MaxLVRPct    float64            `json:"maxLvrPct"`
	OtherCosts   float64            `json:"otherCosts"`
	DutyOverride *float64           `json:"dutyOverride"`
	SharePct     float64            `json:"sharePct"` // % of the monthly surplus committed to the mortgage
	ExcludeRent  bool               `json:"excludeRent"`
	Months       int                `json:"months"`
	Adjust       map[string]float64 `json:"adjust"` // category -> % change (-100..+100)
	ExtraIncome  float64            `json:"extraIncome"`
}

type mortgageOut struct {
	IncomeM        float64            `json:"incomeM"`
	ExpensesM      float64            `json:"expensesM"`
	SurplusM       float64            `json:"surplusM"`
	AvailableM     float64            `json:"availableM"`
	AssessRatePct  float64            `json:"assessRatePct"`
	MaxLoan        float64            `json:"maxLoan"` // by serviceability
	Loan           float64            `json:"loan"`    // actually used (after LVR cap)
	Price          float64            `json:"price"`
	Duty           float64            `json:"duty"`
	OtherCosts     float64            `json:"otherCosts"`
	Down           float64            `json:"down"`
	LVRPct         float64            `json:"lvrPct"`
	Binding        string             `json:"binding"` // serviceability | lvr | deposit
	RepaymentM     float64            `json:"repaymentM"`
	AssessPaymentM float64            `json:"assessPaymentM"`
	LeftOverM      float64            `json:"leftOverM"`
	LMIWarning     bool               `json:"lmiWarning"`
	Adjusted       map[string]float64 `json:"adjusted"` // category -> adjusted monthly
	Warnings       []string           `json:"warnings"`
	DutyStatus     string             `json:"dutyStatus"`
	DutyURL        string             `json:"dutyUrl"`
}

// solveMortgage works out the highest purchase price that the deposit plus the maximum loan can
// pay for once stamp duty and other costs are covered:
//
//	price + duty(price) + otherCosts = down + min(maxLoan, maxLVR*price)
//
// The left side grows faster than the right as price rises, so bisection finds the single answer.
func solveMortgage(in mortgageIn, income float64, lines []baseLine) mortgageOut {
	if in.TermYears <= 0 {
		in.TermYears = 30
	}
	if in.MaxLVRPct <= 0 || in.MaxLVRPct > 98 {
		in.MaxLVRPct = 90
	}
	if in.SharePct <= 0 || in.SharePct > 100 {
		in.SharePct = 100
	}
	out := mortgageOut{Adjusted: map[string]float64{}, Down: in.Down, OtherCosts: in.OtherCosts, IncomeM: income + in.ExtraIncome}
	for _, l := range lines {
		m := l.Monthly
		if l.Category == "Rent" && in.ExcludeRent {
			out.Adjusted[l.Category] = 0
			continue
		}
		factor := 1 + in.Adjust[l.Category]/100
		out.Adjusted[l.Category] = math.Max(0, m*factor)
		out.ExpensesM += out.Adjusted[l.Category]
	}
	out.SurplusM = out.IncomeM - out.ExpensesM
	out.AvailableM = math.Max(0, out.SurplusM) * in.SharePct / 100
	out.AssessRatePct = in.RatePct + in.BufferPct
	out.MaxLoan = pvAnnuity(out.AvailableM, out.AssessRatePct, in.TermYears)

	st := findDutyState(in.State)
	if st != nil {
		out.DutyStatus, out.DutyURL = st.Status, st.URL
	}
	duty := func(p float64) float64 {
		if in.DutyOverride != nil {
			return math.Max(0, *in.DutyOverride)
		}
		return dutyFor(st, in.Buyer, p)
	}
	lvr := in.MaxLVRPct / 100
	loanAt := func(p float64) float64 { return math.Min(out.MaxLoan, lvr*p) }
	f := func(p float64) float64 { return p + duty(p) + in.OtherCosts - in.Down - loanAt(p) }

	switch {
	case f(0) > 0:
		out.Warnings = append(out.Warnings, "The deposit doesn't cover duty and other costs even for a very cheap purchase.")
		out.Price = 0
	default:
		lo, hi := 0.0, 50e6
		for i := 0; i < 100; i++ {
			mid := (lo + hi) / 2
			if f(mid) > 0 {
				hi = mid
			} else {
				lo = mid
			}
		}
		out.Price = math.Floor(lo)
	}
	out.Duty = math.Round(duty(out.Price))
	out.Loan = math.Round(loanAt(out.Price))
	if out.Price > 0 {
		out.LVRPct = out.Loan / out.Price * 100
	}
	switch {
	case out.Price == 0:
		out.Binding = "deposit"
	case out.MaxLoan <= lvr*out.Price+1:
		out.Binding = "serviceability"
	default:
		out.Binding = "lvr"
	}
	out.RepaymentM = pmt(out.Loan, in.RatePct, in.TermYears)
	out.AssessPaymentM = pmt(out.Loan, out.AssessRatePct, in.TermYears)
	out.LeftOverM = out.SurplusM - out.RepaymentM
	out.LMIWarning = out.LVRPct > 80.01

	if out.IncomeM <= 0 {
		out.Warnings = append(out.Warnings, "No income detected in your transactions, so nothing can be borrowed. Check that salary credits are categorised as income.")
	} else if out.SurplusM <= 0 {
		out.Warnings = append(out.Warnings, "Spending is at or above income, leaving nothing to service a loan. Try the sliders below.")
	}
	if out.Binding == "lvr" {
		out.Warnings = append(out.Warnings, "Borrowing is limited by the maximum loan-to-value ratio, not your repayment capacity. A bigger deposit is what raises the price now.")
	}
	if out.LMIWarning {
		out.Warnings = append(out.Warnings, "Loan is above 80% of the price, so lenders mortgage insurance (LMI) would usually apply. It is not included here.")
	}
	return out
}

// ---- baseline from the user's transactions -----------------------------

func (a *app) mortgageBaseline(months int) (income float64, lines []baseLine, rep *report, err error) {
	rep, err = a.buildReport(months)
	if err != nil || rep.Empty {
		return 0, nil, rep, err
	}
	for _, g := range rep.Groups {
		switch g.Name {
		case gIncome:
			income = g.Monthly
		case gNeut: // transfers & savings aren't living costs; unexplained spending is treated as spending
			for _, l := range g.Lines {
				if l.Category == "Uncategorized" && l.Monthly > 0 {
					lines = append(lines, baseLine{l.Category, "Unclassified", l.Monthly})
				}
			}
		default:
			for _, l := range g.Lines {
				lines = append(lines, baseLine{l.Category, g.Name, l.Monthly})
			}
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Monthly > lines[j].Monthly })
	return income, lines, rep, nil
}

// ---- handlers ----------------------------------------------------------

func (a *app) mortgagePage(w http.ResponseWriter, r *http.Request) {
	months := monthsParam(r)
	if r.URL.Query().Get("months") == "" {
		months = 6
	}
	income, lines, rep, err := a.mortgageBaseline(months)
	if err != nil {
		fail(w, err, 500)
		return
	}
	type stateView struct{ Code, Name, Status, URL string }
	var states []stateView
	for _, s := range dutyStates {
		states = append(states, stateView{s.Code, s.Name, s.Status, s.URL})
	}
	a.tpl.ExecuteTemplate(w, "mortgage.html", map[string]any{
		"Empty": rep == nil || rep.Empty,
		"Boot": map[string]any{"income": income, "lines": lines, "states": states, "months": months,
			"from": func() string {
				if rep != nil {
					return rep.From
				}
				return ""
			}(),
			"to": func() string {
				if rep != nil {
					return rep.To
				}
				return ""
			}()},
	})
}

func (a *app) mortgageCalc(w http.ResponseWriter, r *http.Request) {
	var in mortgageIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		fail(w, errBad, 400)
		return
	}
	if in.Months < 1 || in.Months > 24 {
		in.Months = 6
	}
	income, lines, _, err := a.mortgageBaseline(in.Months)
	if err != nil {
		fail(w, err, 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(solveMortgage(in, income, lines))
}
