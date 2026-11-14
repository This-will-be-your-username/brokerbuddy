package main

import (
	"math"
	"testing"
)

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s: got %.2f want %.2f", name, got, want)
	}
}

// Figures below are published worked examples / official tables (see duty.go Status notes).
func TestDutyKnownValues(t *testing.T) {
	d := func(code, buyer string, v float64) float64 { return dutyFor(findDutyState(code), buyer, v) }
	near(t, "NSW 1m", d("NSW", "owner", 1000000), 39187, 1)
	near(t, "QLD 500k general (investor)", d("QLD", "investor", 500000), 15925, 1)
	near(t, "QLD 500k home concession", d("QLD", "owner", 500000), 8750, 1)
	near(t, "VIC 600k general", d("VIC", "investor", 600000), 31070, 1)
	near(t, "VIC 800k", d("VIC", "owner", 800000), 43070, 1)
	near(t, "TAS 700k", d("TAS", "owner", 700000), 26748, 1)
	near(t, "TAS 750k", d("TAS", "owner", 750000), 28935, 1)
	near(t, "WA 750k", d("WA", "owner", 750000), 29741, 1)
	near(t, "SA 750k", d("SA", "owner", 750000), 35080, 1)
	near(t, "ACT 750k", d("ACT", "owner", 750000), 19208, 1)
	near(t, "NT 500k", d("NT", "owner", 500000), 23928.6, 1)
}

func TestDutyContinuityAndMonotonic(t *testing.T) { // catches typos in the bracket tables
	for _, s := range dutyStates {
		prev := 0.0
		for v := 10000.0; v <= 3e6; v += 5000 {
			got := s.General(v)
			// allow VIC's/ACT's published step where the flat rate starts, but nothing larger
			if got < prev-1 && !(s.Code == "VIC" || s.Code == "ACT") {
				t.Errorf("%s duty falls at %.0f: %.0f < %.0f", s.Code, v, got, prev)
			}
			if got < prev-300 {
				t.Errorf("%s duty drops by %.0f at %.0f", s.Code, prev-got, v)
			}
			prev = got
		}
	}
}

func TestFirstHomeBuyerConcessions(t *testing.T) {
	near(t, "NSW fhb 800k", dutyFor(findDutyState("NSW"), "fhb", 800000), 0, 0.01)
	near(t, "NSW fhb 1m = full", dutyFor(findDutyState("NSW"), "fhb", 1000000), 39187, 1)
	near(t, "WA fhb 700k", dutyFor(findDutyState("WA"), "fhb", 700000), 16150, 1)
	near(t, "QLD fhb 700k", dutyFor(findDutyState("QLD"), "fhb", 700000), 0, 0.01)
	// states with no concession fall back to normal duty
	near(t, "SA fhb", dutyFor(findDutyState("SA"), "fhb", 500000), dutyFor(findDutyState("SA"), "owner", 500000), 0.01)
}

func TestAnnuityRoundTrip(t *testing.T) {
	loan := 500000.0
	p := pmt(loan, 6, 30)
	near(t, "pmt", p, 2997.75, 0.5) // standard 6%/30y figure
	near(t, "pv", pvAnnuity(p, 6, 30), loan, 0.5)
	near(t, "zero rate", pvAnnuity(1000, 0, 10), 120000, 0.01)
}

func lines() []baseLine {
	return []baseLine{{"Rent", gEss, 2000}, {"Groceries", gEss, 800}, {"Dining & Takeaway", gDisc, 600}, {"Entertainment", gDisc, 400}}
}

func baseIn() mortgageIn {
	return mortgageIn{Down: 100000, State: "NSW", Buyer: "owner", TermYears: 30, RatePct: 6, BufferPct: 3, MaxLVRPct: 90, OtherCosts: 2000, ExcludeRent: true, Months: 6}
}

func TestSolveBalancesBudget(t *testing.T) {
	out := solveMortgage(baseIn(), 8000, lines()) // spending 1800 after rent removed -> surplus 6200
	near(t, "surplus", out.SurplusM, 6200, 0.01)
	near(t, "assess rate", out.AssessRatePct, 9, 0.001)
	if out.Price <= 0 {
		t.Fatalf("expected a price: %+v", out)
	}
	// funds in = costs out: deposit + loan = price + duty + other costs
	near(t, "identity", out.Down+out.Loan, out.Price+out.Duty+out.OtherCosts, 5)
	if out.LVRPct > 90.01 {
		t.Errorf("LVR %.1f exceeds cap", out.LVRPct)
	}
	// duty must equal the schedule at the solved price
	near(t, "duty", out.Duty, dutyFor(findDutyState("NSW"), "owner", out.Price), 2)
}

func TestSlidersChangePrice(t *testing.T) {
	in := baseIn()
	base := solveMortgage(in, 6000, lines())
	cut := in
	cut.Adjust = map[string]float64{"Dining & Takeaway": -100, "Entertainment": -50}
	less := solveMortgage(cut, 6000, lines())
	if less.Price <= base.Price {
		t.Errorf("cutting spending should raise the price: %.0f vs %.0f", less.Price, base.Price)
	}
	raise := in
	raise.Adjust = map[string]float64{"Groceries": 100}
	more := solveMortgage(raise, 6000, lines())
	if more.Price >= base.Price {
		t.Errorf("raising spending should lower the price: %.0f vs %.0f", more.Price, base.Price)
	}
	near(t, "adjusted line", less.Adjusted["Entertainment"], 200, 0.01)
	near(t, "adjusted to zero", less.Adjusted["Dining & Takeaway"], 0, 0.01)
}

func TestRentExclusion(t *testing.T) {
	in := baseIn()
	in.ExcludeRent = false
	with := solveMortgage(in, 6000, lines())
	in.ExcludeRent = true
	without := solveMortgage(in, 6000, lines())
	if without.Price <= with.Price {
		t.Error("removing rent should raise the price")
	}
}

func TestLVRCapBinds(t *testing.T) {
	in := baseIn()
	in.Down = 20000 // tiny deposit, huge income: LVR (not income) limits borrowing
	out := solveMortgage(in, 30000, lines())
	if out.Binding != "lvr" || out.LVRPct > 90.01 || out.LVRPct < 89 {
		t.Errorf("binding=%s lvr=%.2f", out.Binding, out.LVRPct)
	}
	if !out.LMIWarning {
		t.Error("90% LVR should warn about LMI")
	}
}

func TestNoCapacityMeansDepositOnly(t *testing.T) {
	out := solveMortgage(baseIn(), 1000, lines()) // spending > income
	if out.MaxLoan != 0 || out.Loan != 0 {
		t.Errorf("no loan expected: %+v", out)
	}
	near(t, "price + duty + costs = deposit", out.Price+out.Duty+out.OtherCosts, out.Down, 5)
	if len(out.Warnings) == 0 {
		t.Error("expected a warning")
	}
}

func TestDutyOverride(t *testing.T) {
	in := baseIn()
	zero := 0.0
	in.DutyOverride = &zero
	free := solveMortgage(in, 6000, lines())
	in.DutyOverride = nil
	normal := solveMortgage(in, 6000, lines())
	if free.Price <= normal.Price || free.Duty != 0 {
		t.Errorf("override should lift the price: %+v vs %+v", free.Price, normal.Price)
	}
}

func TestDepositTooSmallForCosts(t *testing.T) {
	in := baseIn()
	in.Down = 500
	out := solveMortgage(in, 6000, lines())
	if out.Price != 0 || len(out.Warnings) == 0 {
		t.Errorf("%+v", out)
	}
}

func TestParseProductRates(t *testing.T) {
	rows := parseProductRates("Test Bank", "p1", "Loan", "https://x", false, []cdrLendingRate{
		{Type: "VARIABLE", Rate: "0.0619", Comparison: "0.0625", Repayment: "PRINCIPAL_AND_INTEREST", Purpose: "OWNER_OCCUPIED"},
		{Type: "FIXED", Rate: 0.0589, AdditionalValue: "P3Y", Repayment: "PRINCIPAL_AND_INTEREST"},
		{Type: "DISCOUNT", Rate: "0.005"},
		{Type: "VARIABLE", Rate: "0.5"}, // 50%: nonsense, dropped
	})
	if len(rows) != 2 {
		t.Fatalf("got %d rows: %+v", len(rows), rows)
	}
	near(t, "rate", rows[0].Rate, 6.19, 0.001)
	near(t, "fixed rate", rows[1].Rate, 5.89, 0.001)
	if rows[1].FixedMonths != 36 {
		t.Errorf("fixed months %d", rows[1].FixedMonths)
	}
}

func TestSpecialAndMislabelledProducts(t *testing.T) {
	if !isSpecialProduct("Flexi First Option Home Loan - Veterans", "", nil) || !isSpecialProduct("Green Home Loan", "", nil) ||
		!isSpecialProduct("Home Loan", "", []string{"Must be a staff member"}) {
		t.Error("special products not flagged")
	}
	if isSpecialProduct("Basic Variable Home Loan", "Low rate, no frills", nil) {
		t.Error("ordinary product flagged")
	}
	rows := parseProductRates("B", "p", "Optimum 3 Year Fixed Rate Home Loan", "", false, []cdrLendingRate{{Type: "VARIABLE", Rate: "0.061"}})
	if len(rows) != 1 || rows[0].Type != "FIXED" || rows[0].FixedMonths != 36 {
		t.Errorf("%+v", rows)
	}
}
