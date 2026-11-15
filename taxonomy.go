package main

import (
	"regexp"
	"sort"
	"strings"
)

// Categories are chosen around what Australian mortgage lenders examine on statements:
// essential vs discretionary living costs, existing debts, income, and "risk flag" items.
type catInfo struct{ Name, Group string }

const (
	gIncome = "Income"
	gEss    = "Essential living"
	gDisc   = "Discretionary"
	gDebt   = "Debts & commitments"
	gRisk   = "Risk flags"
	gNeut   = "Neutral"
)

var taxonomy = []catInfo{
	{"Salary & Wages", gIncome}, {"Government Benefits", gIncome}, {"Rental Income", gIncome}, {"Other Income", gIncome},
	{"Groceries", gEss}, {"Rent", gEss}, {"Home & Strata", gEss}, {"Utilities & Internet", gEss}, {"Insurance", gEss},
	{"Health & Medical", gEss}, {"Transport & Fuel", gEss}, {"Education & Childcare", gEss},
	{"Dining & Takeaway", gDisc}, {"Entertainment", gDisc}, {"Subscriptions", gDisc}, {"Shopping & Clothing", gDisc},
	{"Personal Care", gDisc}, {"Travel & Holidays", gDisc}, {"Alcohol & Tobacco", gDisc}, {"Pets", gDisc}, {"Gifts & Donations", gDisc},
	{"Mortgage & Loan Repayments", gDebt}, {"Credit Card Repayments", gDebt}, {"Buy Now Pay Later", gDebt},
	{"Child Support", gDebt}, {"Tax", gDebt},
	{"Gambling", gRisk}, {"Cash Withdrawals", gRisk}, {"Fees & Dishonours", gRisk}, {"Short-term Credit", gRisk},
	{"Transfers", gNeut}, {"Savings & Investments", gNeut}, {"Uncategorized", gNeut},
}

var groupOf = func() map[string]string {
	m := map[string]string{}
	for _, c := range taxonomy {
		m[c.Name] = c.Group
	}
	return m
}()

func allCategories() []string {
	out := make([]string, 0, len(taxonomy))
	for _, c := range taxonomy {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return out
}

// Pattern dictionary: category -> '|'-separated phrases. Matching is on whole words after
// normalising punctuation, and the LONGEST matching phrase wins ("coles express" beats "coles",
// "atm fee" beats "atm"). Phrases for income categories only apply to credits.
var dictSrc = map[string]string{
	"Groceries":                  "woolworths|coles|aldi|iga|foodland|harris farm|costco|foodworks|supermarket|grocer|fruit and veg|butcher|ritchies|drakes|spar|farmers market|green grocer",
	"Transport & Fuel":           "coles express|woolworths petrol|7 eleven|bp|shell|caltex|ampol|united petroleum|puma energy|metro petroleum|reddy express|liberty oil|opal|myki|go card|gocard|translink|transport for nsw|transurban|linkt|etoll|uber|didi|ola|13cabs|taxi|parking|wilson parking|secure parking|vicroads|service nsw|toll|repco|supercheap auto|autobarn|beaurepaires|bridgestone tyres|mechanic|car wash|racq|rego|e tag|etag|petrol|fuel|service station",
	"Dining & Takeaway":          "mcdonald s|mcdonalds|kfc|hungry jack s|hungry jacks|dominos|domino s|pizza hut|subway|red rooster|nandos|nando s|guzman y gomez|oporto|grill d|uber eats|ubereats|doordash|menulog|deliveroo|starbucks|gloria jeans|zambrero|chatime|boost juice|cafe|coffee|restaurant|kebab|sushi|thai|pizza|bakery|bar and grill|pub|tavern|bistro|takeaway|burger|noodle|ramen|dumpling|gelato|donut|muffin break|michel s|mad mex|betty s burgers",
	"Alcohol & Tobacco":          "dan murphy s|dan murphys|bws|liquorland|first choice liquor|liquor|bottle o|bottlemart|vintage cellars|cellarbrations|tobacco|tobacconist|vape",
	"Utilities & Internet":       "agl|origin energy|energyaustralia|energy australia|red energy|alinta|telstra|optus|vodafone|tpg|aussie broadband|belong|superloop|iinet|amaysim|boost mobile|lebara|sydney water|water corporation|yarra valley water|ausgrid|citipower|powercor|ergon|nbn|dodo|exetel|lycamobile|kogan mobile|circles life|electricity|gas bill|water bill|internet|mobile plan",
	"Insurance":                  "nrma insurance|racv|raa insurance|aami|allianz|budget direct|youi|suncorp|medibank|bupa|hcf|nib|ahm|hbf|cgu|qbe|real insurance|ando|tower insurance|insurance|assurance|ambulance",
	"Health & Medical":           "chemist warehouse|priceline pharmacy|terry white|amcal|chemist|pharmacy|medical|doctor|dental|dentist|physio|optometrist|specsavers|opsm|pathology|sonic healthcare|laverty|hospital|medicare|anytime fitness|fitness first|goodlife|gym|snap fitness|f45|jetts|psychology|radiology|healthscope|osteo|chiropract|podiatr|skin clinic",
	"Home & Strata":              "bunnings|strata|body corporate|council rates|council|rates notice|mitre 10|hardware|ikea|plumber|electrician|handyman|lawn|pest control|locksmith|home maintenance|garden",
	"Education & Childcare":      "childcare|child care|day care|daycare|kindergarten|school|tafe|university|tuition|kumon|goodstart|c k|early learning|out of school hours|oshc|textbook|unibooks",
	"Shopping & Clothing":        "kmart|target|big w|myer|david jones|uniqlo|h m|cotton on|just jeans|rebel sport|jb hi fi|harvey norman|the good guys|amazon|ebay|temu|shein|aliexpress|officeworks|best and less|country road|witchery|zara|bonds|nike|adidas|the iconic|kogan|bed bath n table|freedom furniture|fantastic furniture|spotlight|lovisa|typo|dymocks|big w|bargain|chemist outlet|apple store|dell",
	"Subscriptions":              "netflix|spotify|disney plus|disney|stan|binge|foxtel|kayo|paramount|amazon prime|prime video|youtube premium|apple com bill|google storage|google one|icloud|microsoft|adobe|dropbox|openai|chatgpt|audible|kindle|patreon|nordvpn|1password|canva|github|subscription|membership",
	"Entertainment":              "event cinemas|hoyts|village cinemas|reading cinemas|ticketek|ticketmaster|moshtix|steam|playstation|nintendo|xbox|timezone|strike bowling|zone bowling|bowling|golf|cinema|theatre|museum|zoo|aquarium|luna park|topgolf|escape room|concert|festival|eventbrite|oztix",
	"Personal Care":              "hairdresser|barber|salon|beauty|nails|massage|sephora|mecca|lush|the body shop|priceline|day spa|waxing|tanning",
	"Travel & Holidays":          "qantas|jetstar|virgin australia|air new zealand|emirates|singapore airlines|booking com|airbnb|expedia|hotels com|webjet|flight centre|trivago|agoda|wotif|motel|resort|qantas holidays|avis|hertz|budget rent a car|thrifty|europcar|sixt|airport|travelex|stayz|rent a car|cruise|hostel",
	"Pets":                       "petbarn|petstock|vet|veterinary|animal hospital|pet circle|greencross|pet shop",
	"Gifts & Donations":          "red cross|oxfam|unicef|world vision|salvation army|cancer council|donation|gofundme|gift|florist|flowers|interflora|lifeline|wwf|uniting|charity",
	"Gambling":                   "sportsbet|tab|ladbrokes|neds|bet365|pointsbet|unibet|betfair|bluebet|palmerbet|topsport|betr|dabble|crown casino|star casino|casino|pokies|lotto|oz lotteries|tatts|tattersalls|keno|lottery|poker|bookmaker|wagering|betting|the lott|sky city|bingo",
	"Buy Now Pay Later":          "afterpay|zip pay|zippay|zip money|zip co|humm|klarna|latitude pay|laybuy|openpay|payright|sezzle|buy now pay later|bnpl",
	"Short-term Credit":          "payday loan|cash converters|nimble|wallet wizard|cash train|money3|ferratum|fast cash|cash advance|pawn|credit corp|collection agency|debt collector",
	"Mortgage & Loan Repayments": "mortgage|home loan|loan repayment|loan payment|personal loan|car loan|novated lease|car finance|loan interest|loan instalment|student loan|helpex|hecs|pepper money|liberty financial|latitude finance|resimac|firstmac|plenti|societyone|wisr",
	"Credit Card Repayments":     "credit card|american express|amex|card repayment|cc payment|visa payment|mastercard payment|card payment",
	"Child Support":              "child support",
	"Tax":                        "ato|australian taxation|tax office|tax payment|bas payment|payg",
	"Rent":                       "rent|rental|real estate|realestate|ray white|lj hooker|raine and horne|raine horne|harcourts|century 21|belle property|mcgrath|first national|nelson alexander|property management|landlord|tenant",
	"Cash Withdrawals":           "atm|cash withdrawal|atm withdrawal|cash out|cashout|cardless cash|cash advance atm",
	"Fees & Dishonours":          "atm fee|overdrawn fee|dishonour|dishonor|nsf|late fee|overdrawing|account fee|monthly fee|service fee|account keeping fee|international transaction fee|foreign transaction fee|overseas transaction fee|over limit fee|overdrawn|unarranged",
	"Transfers":                  "transfer to|transfer from|internal transfer|osko|payid|fast transfer|pay anyone|own account|transfer|bpay transfer|mobile banking transfer|online transfer|funds transfer",
	"Savings & Investments":      "transfer to savings|transfer from savings|savings goal|term deposit|commsec|selfwealth|stake|superhero|raiz|spaceship|vanguard|betashares|pearler|coinspot|binance|swyftx|independent reserve|superannuation|super contribution|investment|share trading|savings maximiser|round up",
	"Salary & Wages":             "salary|wages|payroll|net pay|pay run|wage|employer",
	"Government Benefits":        "centrelink|services australia|ndis|jobseeker|youth allowance|austudy|child care subsidy|family tax|dept human services|dhs|medicare benefit|parenting payment|carer payment|age pension|disability support",
	"Other Income":               "interest paid|credit interest|interest earned|interest|dividend|tax refund|refund ato|cashback|bonus interest|reimbursement|insurance claim|centrepay",
}

type dictEntry struct {
	pat, compact string
	cat          string
	words        int
}

var dict = func() []dictEntry {
	var out []dictEntry
	for cat, s := range dictSrc {
		for _, p := range strings.Split(s, "|") {
			p = norm(p)
			if p == "" {
				continue
			}
			out = append(out, dictEntry{p, strings.ReplaceAll(p, " ", ""), cat, strings.Count(p, " ") + 1})
		}
	}
	// deterministic: longest first, then alphabetical
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].pat) != len(out[j].pat) {
			return len(out[i].pat) > len(out[j].pat)
		}
		return out[i].pat < out[j].pat
	})
	return out
}()

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func norm(s string) string {
	return strings.TrimSpace(nonAlnum.ReplaceAllString(strings.ToLower(s), " "))
}

func isIncomeCat(c string) bool { return groupOf[c] == gIncome }

// dictLookup returns the longest matching phrase's category. Credits may match anything
// (a refund keeps its spend category); debits can't be income categories.
func dictLookup(text string, amount float64) (cat, phrase string, glued, ok bool) {
	t := " " + text + " "
	compact := strings.ReplaceAll(text, " ", "")
	for _, e := range dict { // sorted longest-first, so first hit is best
		if amount > 0 && isIncomeCat(e.cat) {
			continue
		}
		whole := strings.Contains(t, " "+e.pat+" ")
		if whole || (len(e.compact) >= 7 && strings.Contains(compact, e.compact)) {
			cat := e.cat
			if cat == "Rent" && amount < 0 {
				cat = "Rental Income"
			}
			return cat, e.pat, !whole, true
		}
	}
	return "", "", false, false
}

func dictMatch(text string, amount float64) (string, bool) {
	cat, _, _, ok := dictLookup(text, amount)
	return cat, ok
}

// ---- merchant name cleanup (used for lookup keys) -------------------------

var stopTokens = map[string]bool{
	"eftpos": true, "purchase": true, "visa": true, "mastercard": true, "debit": true, "card": true, "direct": true,
	"bpay": true, "osko": true, "payment": true, "pos": true, "tap": true, "contactless": true, "value": true,
	"date": true, "receipt": true, "paypal": true, "sq": true, "pty": true, "ltd": true, "limited": true,
	"nsw": true, "vic": true, "qld": true, "wa": true, "sa": true, "tas": true, "act": true, "nt": true,
	"aus": true, "aud": true, "to": true, "from": true, "the": true, "of": true, "and": true, "in": true,
	"online": true, "internet": true, "withdrawal": true, "au": true, "ref": true, "no": true, "pay": true,
}

// merchantKey reduces a bank narrative to the 1-2 words that identify the business.
func merchantKey(desc string) string {
	var keep []string
	for _, tok := range strings.Fields(norm(desc)) {
		if stopTokens[tok] || len(tok) < 3 || strings.ContainsAny(tok, "0123456789") {
			continue
		}
		keep = append(keep, tok)
		if len(keep) == 2 {
			break
		}
	}
	return strings.Join(keep, " ")
}

// ---- classifier -------------------------------------------------------------

type cacheEntry struct{ Category, Source, Detail string }

type classifier struct {
	rules []rule
	cache map[string]cacheEntry
	learn *learner // examples from your manual categorisations (nil-safe)
	hist  *history // trustworthy categorised transactions, for similarity guesses (nil-safe)
}

// Plaid's coarse categories, mapped onto ours (used only as a last-resort hint).
var pfcMap = map[string]string{
	"INCOME": "Other Income", "FOOD_AND_DRINK": "Dining & Takeaway", "TRANSPORTATION": "Transport & Fuel",
	"GENERAL_MERCHANDISE": "Shopping & Clothing", "RENT_AND_UTILITIES": "Utilities & Internet",
	"ENTERTAINMENT": "Entertainment", "MEDICAL": "Health & Medical", "PERSONAL_CARE": "Personal Care",
	"TRAVEL": "Travel & Holidays", "HOME_IMPROVEMENT": "Home & Strata", "TRANSFER_IN": "Transfers",
	"TRANSFER_OUT": "Transfers", "LOAN_PAYMENTS": "Mortgage & Loan Repayments", "BANK_FEES": "Fees & Dishonours",
	"GOVERNMENT_AND_NON_PROFIT": "Gifts & Donations",
}
