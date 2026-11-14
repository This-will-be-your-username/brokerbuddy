package main

import "math"

// Stamp (transfer) duty for buying a home, by state/territory.
//
// Rules change often (several states changed concessions in 2026), so each state carries a
// Status describing how well the numbers were checked, and the Mortgage page always lets you
// type your own duty figure from the official calculator (URL).

type bracket struct {
	Over, Base, Rate float64 // duty = Base + (v-Over)*Rate for the last bracket with Over <= v; Base = -1 means duty = Rate*v
}

func scheduleDuty(bs []bracket, v float64) float64 {
	if v <= 0 {
		return 0
	}
	b := bs[0]
	for _, c := range bs {
		if v >= c.Over {
			b = c
		}
	}
	if b.Base < 0 { // Base < 0 marks a flat percentage of the whole value
		return b.Rate * v
	}
	return b.Base + (v-b.Over)*b.Rate
}

type dutyState struct {
	Code, Name string
	General    func(v float64) float64
	// Owner is the owner-occupier (principal residence) schedule where it differs from General.
	Owner func(v float64) float64
	// FHB gives duty for an eligible first home buyer, given the owner-occupier duty at that price.
	FHB    func(v, base float64) float64
	Status string
	URL    string
}

func taper(v, exempt, cap, base float64) float64 { // 0 up to exempt, linear up to full duty at cap
	switch {
	case v <= exempt:
		return 0
	case v >= cap:
		return base
	default:
		return base * (v - exempt) / (cap - exempt)
	}
}

var nswBrackets = []bracket{{0, 0, 0.0125}, {18000, 225, 0.015}, {38000, 525, 0.0175}, {103000, 1662, 0.035}, {387000, 11602, 0.045}, {1290000, 52237, 0.055}}
var qldGeneral = []bracket{{0, 0, 0}, {5000, 0, 0.015}, {75000, 1050, 0.035}, {540000, 17325, 0.045}, {1000000, 38025, 0.0575}}
var qldHome = []bracket{{0, 0, 0.01}, {350000, 3500, 0.035}, {540000, 10150, 0.045}, {1000000, 30850, 0.0575}}
var vicGeneral = []bracket{{0, 0, 0.014}, {25000, 350, 0.024}, {130000, 2870, 0.06}, {960000, -1, 0.055}, {2000000, 110000, 0.065}}
var vicPPR = []bracket{{0, 0, 0.014}, {25000, 350, 0.024}, {130000, 2870, 0.05}, {440000, 18370, 0.06}}
var saBrackets = []bracket{{0, 0, 0.01}, {12000, 120, 0.02}, {30000, 480, 0.03}, {50000, 1080, 0.035}, {100000, 2830, 0.04},
	{200000, 6830, 0.0425}, {250000, 8955, 0.0475}, {300000, 11330, 0.05}, {500000, 21330, 0.055}}
var waBrackets = []bracket{{0, 0, 0.019}, {120000, 2280, 0.0285}, {150000, 3135, 0.038}, {360000, 11115, 0.0475}, {725000, 28453, 0.0515}}
var tasBrackets = []bracket{{0, 50, 0}, {3000, 50, 0.0175}, {25000, 435, 0.0225}, {75000, 1560, 0.035}, {200000, 5935, 0.04},
	{375000, 12935, 0.0425}, {725000, 27810, 0.045}}
var actBrackets = []bracket{{0, 0, 0.0028}, {260000, 728, 0.022}, {300000, 1608, 0.034}, {500000, 8408, 0.0432}, {750000, 19208, 0.059},
	{1000000, 33958, 0.064}, {1455000, -1, 0.0454}}

func ntDuty(v float64) float64 {
	switch {
	case v <= 525000:
		k := v / 1000
		return 0.06571441*k*k + 15*k
	case v <= 3000000:
		return 0.0495 * v
	case v <= 5000000:
		return 0.0575 * v
	default:
		return 0.0595 * v
	}
}

func sched(bs []bracket) func(float64) float64 {
	return func(v float64) float64 { return scheduleDuty(bs, v) }
}

var dutyStates = []dutyState{
	{Code: "NSW", Name: "New South Wales", General: sched(nswBrackets),
		FHB:    func(v, base float64) float64 { return taper(v, 800000, 1000000, base) },
		Status: "General rates: Revenue NSW 2026/27 (official). First home buyer: full exemption to $800k is official; the $800k–$1M concession is approximated as a straight-line phase-in.",
		URL:    "https://www.revenue.nsw.gov.au/_resources/duties-links/calculate-your-transfer-duty"},
	{Code: "VIC", Name: "Victoria", General: sched(vicGeneral),
		Owner: func(v float64) float64 {
			if v <= 550000 {
				return scheduleDuty(vicPPR, v)
			}
			return scheduleDuty(vicGeneral, v)
		},
		FHB:    func(v, base float64) float64 { return taper(v, 600000, 750000, base) },
		Status: "General rates match published examples. Principal-residence rates (to $550k) and the first home buyer concession ($600k exempt, $750k cap, phase-in approximated) are NOT confirmed for 2026-27 and the first home buyer relief may have expired on 30 June 2026. Verify.",
		URL:    "https://www.sro.vic.gov.au"},
	{Code: "QLD", Name: "Queensland", General: sched(qldGeneral), Owner: sched(qldHome),
		FHB:    func(v, base float64) float64 { return taper(v, 700000, 800000, base) },
		Status: "General rates: Queensland Revenue Office (official, June 2026). Home concession rates and first home limits ($700k exempt to $800k) are from earlier years; a 2026 Act changed home ownership rules, so verify.",
		URL:    "https://qro.qld.gov.au/duties/transfer-duty/calculate/"},
	{Code: "WA", Name: "Western Australia", General: sched(waBrackets),
		FHB: func(v, base float64) float64 {
			switch {
			case v <= 600000:
				return 0
			case v <= 800000:
				return (v - 600000) * 0.1615
			}
			return base
		},
		Status: "First home owner rate (nil to $600k, then $16.15 per $100 to $800k) is from the WA Government page, effective 7 May 2026. General rates match published figures. Subject to legislation commencing.",
		URL:    "https://www.wa.gov.au/organisation/department-of-treasury-and-finance/transfer-duty-assessment"},
	{Code: "SA", Name: "South Australia", General: sched(saBrackets),
		Status: "Rates cross-checked against RevenueSA figures. No first home buyer duty relief on established homes (new homes may be exempt: enter your own figure).",
		URL:    "https://www.revenuesa.sa.gov.au"},
	{Code: "TAS", Name: "Tasmania", General: sched(tasBrackets),
		Status: "Rates match published worked examples. The temporary first home buyer exemption for established homes ended 30 June 2026.",
		URL:    "https://www.sro.tas.gov.au"},
	{Code: "ACT", Name: "Australian Capital Territory", General: sched(actBrackets),
		FHB:    func(v, base float64) float64 { return 0 },
		Status: "Owner-occupier schedule is the 2025-26 one (matches a published $750k example); 2026-27 may differ. Home Buyer Concession Scheme modelled as nil duty (income and price caps removed from 1 July 2026): confirm eligibility.",
		URL:    "https://www.revenue.act.gov.au/rates-and-property-charges/conveyance-duty-stamp-duty/about-conveyance-duty-stamp-duty"},
	{Code: "NT", Name: "Northern Territory", General: ntDuty,
		Status: "Statutory formula from published sources. The Territory Home Owner Discount (a reduction for owner-occupiers) is not modelled, so duty here may be overstated.",
		URL:    "https://treasury.nt.gov.au"},
}

func findDutyState(code string) *dutyState {
	for i := range dutyStates {
		if dutyStates[i].Code == code {
			return &dutyStates[i]
		}
	}
	return nil
}

// dutyFor returns the duty for a purchase price. buyer: "owner" | "fhb" | "investor".
func dutyFor(s *dutyState, buyer string, v float64) float64 {
	if s == nil || v <= 0 {
		return 0
	}
	base := s.General(v)
	if buyer != "investor" && s.Owner != nil {
		base = s.Owner(v)
	}
	if buyer == "fhb" && s.FHB != nil {
		return math.Max(0, s.FHB(v, base))
	}
	return math.Max(0, base)
}
