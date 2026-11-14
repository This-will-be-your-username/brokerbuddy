# BrokerBuddy

Local Go web app: connect bank accounts via [Plaid](https://plaid.com), pull transactions, auto-categorize them.

## Run

1. Get free Sandbox keys at https://dashboard.plaid.com (Developers → Keys).
2. Start the app (PowerShell):

```powershell
$env:PLAID_CLIENT_ID="..."; $env:PLAID_SECRET="..."; $env:PLAID_ENV="sandbox"
$env:APP_SECRET="long-random-string"   # encrypts bank tokens in the DB; keep it stable
go run .
```

3. Open https://127.0.0.1:8443 (self-signed cert, so accept the browser warning once), click **Connect a bank**. In Sandbox use `user_good` / `pass_good`.

`PLAID_ENV` can be `sandbox` or `production` (production needs Plaid approval). Other vars: `ADDR`, `DB_PATH`.

## HTTPS

- **Local (default):** in-memory self-signed cert on `127.0.0.1:8443`.
- **Let's Encrypt:** point a DNS record at the machine, open ports 80 and 443, then set `DOMAIN=bank.example.com`, `APP_PASSWORD=...` (HTTP basic auth, user `admin` or `APP_USER`) and optionally `ACME_EMAIL`. Certs are fetched and renewed automatically and cached in `CERT_DIR` (default `certs/`). Set `ACME_STAGING=1` first to avoid rate limits.
- **Your own cert:** `TLS_CERT` and `TLS_KEY` file paths; `ADDR` to change the bind address.

A non-loopback `ADDR` or `DOMAIN` refuses to start without `APP_PASSWORD`.

## CSV import (no bank API needed)

Click **Import CSV**, upload an export from any bank. The app skips preamble lines, sniffs the delimiter, finds the header row, and guesses the date, description, amount (or separate debit/credit) columns and the date format; fix anything wrong with the dropdowns and check the preview. Mappings are remembered per header layout, so the next export from the same bank is one click. Re-importing overlapping exports skips duplicates. Amounts with a decimal comma (`4,50`) are not supported.

## Categories and the lender view

Transactions are bucketed into ~30 categories grouped the way an Australian lender reads statements: Income, Essential living, Discretionary, Debts & commitments, Risk flags (gambling, cash withdrawals, dishonours/fees, short-term credit) and Neutral (transfers, savings).

Classification order: your rules, then a built-in dictionary of AU merchants and bank phrases (longest phrase wins, whole words only, direction-aware so a salary credit is income but a debit is not), then cached online lookups, then the bank's own category.

**Learning from your edits:** every category you set by hand becomes an example. Similar transactions (same business name, ignoring store numbers and suburbs, tolerating typos like `ZORBA`/`ZORBAS`) follow automatically and are marked `≈`. An example that fully matches beats the built-in dictionary; a brand-only match (e.g. you categorised `COLES EXPRESS`, a new one says `COLES`) is used only where the dictionary has no answer, and is skipped if your examples for that brand disagree. Your explicit rules still win over everything, and a debit is never learned as income.

**Unknown merchants:** click *Look up unknown merchants*. It sends only cleaned business names (e.g. `bunnings`) to Wikipedia's free API, 12 per click, biggest spend first. Results are marked `?`, listed on the home page, and can be rejected. Wikipedia knows chains, not local shops: for those, set the category once and tick *rule*.

**Lender view** (`/report`): 3/6/12-month averages by group and category, % of income, surplus, and a list of things an assessor may notice. CSV download included. It is a preparation aid, not a serviceability calculation.

## Tabs

- **Dashboard** (`/`): income, spending, surplus and savings rate for 1/3/6/12 months or all time, monthly income-vs-expenses chart, top spending categories, recent transactions.
- **Transactions** (`/transactions`): the full list, inline category editing, rules, imported accounts, merchant lookups.
- **Charts** (`/charts`): filter by period, accounts, groups, categories, income/spending, search text, and day/week/month grouping. Seven views: income vs spending, stacked spending by category, category share (click a slice to filter), group split, top merchants, cumulative net, spending by weekday, plus the largest transactions. Filtering happens in the browser, so it is instant.
- **Mortgage** (`/mortgage`): see below.
- **Lender view** (`/report`) and **Import CSV** (`/import`).

## Mortgage tab

1. Enter your deposit, state, buyer type (owner-occupier, first home buyer, investor), term, and either type a rate or pick one from the market list.
2. **Borrowing capacity** = your average monthly surplus (income minus spending from your transactions) that can service a loan at *rate + stress buffer* (default +3 points, like lenders use). Current rent is removed by default, since it stops when you buy. Existing loans, credit cards and BNPL stay in as spending.
3. **Purchase price** solves `price + stamp duty + other costs = deposit + loan`, with the loan capped by your maximum LVR.
4. **Sliders** change each spending category from -100% to +100% in 1% steps; the price recalculates as you drag.

**Rates.** *Search for current rates* downloads every home-loan rate that Australian banks, credit unions and building societies publish under the Consumer Data Right (free, no key; about 90 lenders, ~8,000 rates, ~1 minute). Staff, veteran, green-loan and other special-eligibility products and rate outliers are hidden unless you tick the box. Mortgage brokers don't publish a feed, so they can't be listed; they place loans with these same lenders.

**Stamp duty** schedules are built in for every state and territory, each with a status note saying how well it was checked (NSW is official 2026/27; several concessions changed in 2026 and are flagged *verify*). You can always type your own duty figure from the state's official calculator. First-home-buyer concession bands marked approximate use straight-line phase-ins.

**Not modelled:** lenders mortgage insurance, minimum living-expense benchmarks (HEM), income shading (e.g. 80% of rental income), HECS and credit-card limits. Treat the result as a planning aid.

## How it works

- Plaid Link (browser) → public token → server exchanges for an access token, stored AES-GCM encrypted in SQLite.
- `/transactions/sync` (cursor-based) pulls added/modified/removed transactions atomically.
- Categorization priority: your rules → built-in keywords → Plaid's category → Uncategorized.
- Edit a category inline; tick "rule" to learn from it. Manual edits survive re-syncs.

## Security notes

- Single-user. Loopback needs no login; any public mode requires `APP_PASSWORD`. Cross-origin POSTs are rejected.
- Losing `APP_SECRET` makes stored tokens unreadable (reconnect banks).

## Test

```
go test ./...
```
