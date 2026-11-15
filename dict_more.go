package main

import "strings"

// Additional static merchant rules. These were added after profiling a real year of uncategorised
// statement lines: the biggest gaps were everyday words that identify a type of business (butcher,
// deli, patisserie, physiotherapy, cellars, swim school...), national brands and billing platforms
// (health funds, ISPs, Ezypay, Spriggy, airlines), and the payment-processor prefixes that wrap
// small local merchants. Local business names are deliberately NOT listed: words and national
// brands generalise, a suburb's shops do not. Add your own local merchants as rules in Settings.
//
// Matching is on whole words after punctuation is stripped, longest phrase wins, so "grill d" beats
// "grill". Ordinary words score lower (see genericPhrases) than brand names.
var dictMore = map[string]string{
	"Groceries": "ww metro|meat|meats|deli|delicatessen|seafood|fish market|fishmonger|greengrocer|green grocer|fruit and vegetable|produce|asian mart|mart|bulk foods|whole foods|wholefoods|organic|supa iga|metro grocer|fresh market|farmers market",

	"Dining & Takeaway": "arabica|bake|bakehouse|baker|bakers|baguette|patisserie|pastry|pastries|boulangerie|bagel|bagels|espresso|roasters|roastery|coffee roasters|tandoori|taco|tacos|falafel|chargrill|char grill|grill|eatery|pizzeria|trattoria|osteria|cantina|tapas|brasserie|izakaya|yakitori|pho|wok|banh mi|dumplings|dim sum|yum cha|ice cream|icecream|juice|smoothie|gong cha|sharetea|bubble tea|milk tea|yoghurt|yogurt|sandwich|sandwiches|gyg|guzman|grilld|grill d|salad|sumo salad|poke|bbq|barbecue|canteen|tgtg|too good to go|foodora|hotel|brewery|brewing|taphouse|noodles|kebabs|shawarma|souvlaki|gozleme|fish and chips|burger|burgers|dessert|desserts|creamery|gelateria|cake|cakes|udon|katsu|bento|diner|dining",

	"Alcohol & Tobacco": "wine|cellars|cellar|wines|winery|vineyard|distillery|bottle shop|bottleshop|thirsty camel|porters liquor|tobacconist",

	"Health & Medical": "physiotherapy|physiotherapist|chiropractic|chiropractor|osteopathy|osteopath|podiatry|podiatrist|psychology|psychologist|psychiatry|psychiatrist|counselling|dermatology|dermatologist|pathology|radiology|medical imaging|imaging|ultrasound|audiology|hearing|optical|eyecare|eye care|orthodontics|orthodontist|periodontist|medical centre|medical center|health centre|health center|clinic|clinics|naturopath|acupuncture|myotherapy|speech pathology|occupational therapy|sleep clinic|day hospital|private hospital|fitness|fit training|pilates|yoga|crossfit|boxing|martial arts|jiu jitsu|bjj|les mills|ezypay|ezidebit|health club",

	"Insurance": "gu health|teachers health|defence health|police health|australian unity|cbhs|frank health|peoplecare|health partners|rt health|navy health|onemedifund|doctors health fund|emergency services health|westfund|cua health|latrobe health|phoenix health|uhf|qld country health|hif|grouphealth|health insurance|private health|health fund|petsure|petplan|pet insurance|greenslip|green slip|compulsory third party|aia|tal life|clearview|onepath|zurich|chubb|vero|gio|apia|shannons|hollard|real insurance|1st for women|woolworths insurance|coles insurance|bingle|insure|insurer",

	"Pets": "small animal|animal specialist|animal specialists|animal clinic|animal care|veterinarian|pet|pets|paws|groomer|grooming|dog wash|kennel|cattery|puppy|dog training|pet food",

	"Education & Childcare": "spriggy|spriggy schools|schools|college|tutoring|kinder|preschool|pre school|before and after school|vacation care|holiday program|camp australia|ymca|young mens christian|scouts|girl guides|swim school|swimming|swim|gymnastics|ballet|dance|dancing|karate|taekwondo|judo|tennis|basketball|netball|cricket|football|soccer|rugby|hockey|athletics|little athletics|music lessons|music school|piano|violin|art class|coding|robotics",

	"Entertainment": "raging waters|water park|waterpark|theme park|dreamworld|movie world|sea world|wet n wild|luna park|taronga|sealife|flight club|stadium|arena|entertainment centre|trampoline|ninja|laser tag|lazer|escape room|bowlers|mini golf|putt putt|skate|skating|ice rink|ski|snow|splash|aquatic centre|leisure centre|cinemas|imax|dendy|palace cinemas|stubhub|viagogo|humanitix|trybooking|try booking|gallery|art gallery|opera|epic games|blizzard|riot games|roblox|minecraft|mojang|ubisoft",

	"Shopping & Clothing": "apple com|globale|global e|shopping centre|shopping center|westfield|athletes foot|foot locker|sportsgirl|portmans|dotti|supre|jd sports|platypus|hype dc|universal store|glue store|lorna jane|kathmandu|macpac|anaconda|bcf|bulk nutrients|musashi|myprotein|sports store|sporting goods|sports shop|australia post|auspost|post office|aus post|starshipit|newsagency|newsagent|newsxpress|stationery|pillow talk|adairs|spotlight|lincraft|toyworld|toy world|smiggle|lego|k mart|mimco|prouds|michael hill|pandora|swarovski|sunglass hut|bookshop|book store|bookstore|books|kinokuniya|booktopia|etsy|kogan|paypal",

	"Utilities & Internet": "wooliesmobile|woolies mobile|woolworths mobile|leaptel|launtel|internode|spintel|tangerine|more telecom|moose mobile|felix mobile|vaya|skymesh|southern phone|activ8me|starlink|powershop|momentum energy|simply energy|lumo energy|globird|sumo power|amber electric|ovo energy|click energy|endeavour energy|jemena|sa power networks|hunter water|icon water|urban utilities|unitywater|sa water|trugas|telco|telecom|broadband",

	"Travel & Holidays": "crowne plaza|hilton|marriott|hyatt|sofitel|novotel|ibis|mercure|ramada|holiday inn|intercontinental|radisson|sheraton|westin|oaks hotels|peppers|rydges|quest apartments|adina|best western|comfort inn|quality hotel|mantra|united airlines|delta air lines|american airlines|british airways|cathay pacific|qatar airways|etihad|lufthansa|air canada|rex airlines|regional express|tigerair|scoot|airasia|fiji airways|hawaiian airlines|airline|airlines|airways|hipcamp|airalo|youtrip|you technologies|klook|viator|getyourguide|tripadvisor|camping|campground|caravan park|holiday park|big4|discovery parks|nrma parks|lodge|hostel|travel insurance|travel agent|flight|flights|ferry|ferries|sealink|spirit of tasmania",

	"Transport & Fuel": "grab|lime|e scooter|sydney trains|nsw trains|transdev|busways|ventura|metro trains|care park|carpark|car park|parkd|viva energy|evie|chargefox|tesla|ev charging|supercharger|car rental|tyre|tyres|auto parts|automotive|roadside|ride share|rideshare",

	"Home & Strata": "hipages|airtasker|home hardware|total tools|sydney tools|reece|tradelink|plumbing|plumbers|electrical|air conditioning|aircon|roofing|gutter|painter|painting|landscaping|landscape|nursery|garden centre|pool supplies|poolwerx|pest control|termite|cleaning|cleaners|skip bin|removalist|removals|kennards|fencing|flooring|carpet|tiles|tile|blinds|curtains|furniture|temple and webster",

	"Personal Care": "hair salon|hair studio|hair design|hair co|hairdressing|hairhouse|hair|barbers|barbershop|brow|brows|lash|lashes|nail bar|laser clinic|laser hair|cosmetic|adore beauty|tattoo|tattoos|piercing",

	"Gifts & Donations": "unicef|oxfam|world vision|wwf|greenpeace|cancer council|beyond blue|vinnies|st vincent|salvos|anglicare|donate|donations|go fund me|bloom|bouquet|hallmark|red balloon|scoopon|gift card|giftcard|gift cards",

	"Subscriptions": "paddle net|paddle|substack|notion|slack|zoom|duolingo|udemy|coursera|skillshare|masterclass|calm|headspace|strava|twitch|crunchyroll|lastpass|bitwarden|expressvpn|proton|midjourney|anthropic|claude ai|gitlab|digitalocean|amazon web services|aws|cloudflare|namecheap|godaddy|squarespace|wix|shopify|figma|linkedin|onedrive|google play|google workspace|microsoft 365|office 365|itunes|app store",

	"Fees & Dishonours": "interest charged|interest charge|purchase interest|cash advance fee|annual fee|card fee|late payment fee|currency conversion|service charge|bank fee|transaction fee|withdrawal fee|fines|infringement|penalty notice|state debt recovery",

	"Transfers": "skrill|neteller|paysafe|wise|revolut|airwallex|remitly|western union|moneygram|ofx|worldremit",

	// BPAY payments to the card issuer: the payment side of a credit card
	"Credit Card Repayments": "anz cards|cba cards|commbank cards|nab cards|westpac cards|citi cards|bankwest cards|st george cards|stgeorge cards|hsbc cards|bendigo cards|suncorp cards|latitude card|qantas money card|virgin money card",

	"Savings & Investments": "coinjar|kraken|btc markets|nabtrade|ishares",
}

// genericMoreSrc lists the ordinary words above: they identify a TYPE of business, so a match is
// plausible but not certain (see phraseConf).
const genericMoreSrc = "apple com|wine|meat|meats|deli|delicatessen|seafood|fish market|produce|organic|mart|bake|baker|bakers|patisserie|pastry|pastries|espresso|roasters|roastery|grill|eatery|tacos|taco|juice|smoothie|salad|canteen|hotel|brewery|brewing|noodles|burger|burgers|dessert|desserts|cake|cakes|diner|dining|" +
	"cellars|cellar|wines|winery|vineyard|physiotherapy|physiotherapist|chiropractic|chiropractor|osteopathy|osteopath|podiatry|podiatrist|psychology|psychologist|counselling|dermatology|imaging|optical|clinic|clinics|fitness|pilates|yoga|crossfit|boxing|martial arts|hearing|" +
	"schools|college|swim|swimming|gymnastics|ballet|dance|dancing|tennis|basketball|netball|cricket|football|soccer|rugby|hockey|athletics|piano|violin|coding|robotics|arena|stadium|skate|skating|ski|snow|gallery|opera|" +
	"books|bookshop|hair|hairdressing|brow|brows|lash|lashes|cosmetic|tattoo|tattoos|nursery|painting|painter|cleaning|cleaners|furniture|flooring|carpet|tiles|tile|blinds|curtains|fines|pet|pets|" +
	"camping|flight|flights|lodge|ferry|ferries|tyre|tyres|automotive|carpark|car park|telco|telecom|broadband|insure|insurer|health insurance|private health|health fund|" +
	"airline|airlines|airways|stationery|newsagency|newsagent|plumbing|plumbers|electrical|landscaping|landscape|removals|donate|donations|hostel|health centre|health center|medical centre|medical center"

// Names that are also ordinary words, or businesses that serve several purposes: plausible, not certain.
var ambiguousMoreSrc = "peppers|skrill|neteller|paysafe|grab|lime|tesla|ezypay|ezidebit|ymca|young mens christian|zoom|calm|notion|slack|scoot|wise|paddle|proton|figma|kraken|bloom|vero|gio|apia|aia|hif|uhf"

// phrases whose match deserves a fixed, lower confidence
var lowConfPhrases = map[string]int{"paypal": 55}

func init() {
	for _, w := range strings.Split(genericMoreSrc, "|") {
		genericPhrases[norm(w)] = true
	}
	for _, w := range strings.Split(ambiguousMoreSrc, "|") {
		ambiguousPhrases[norm(w)] = true
	}
}
