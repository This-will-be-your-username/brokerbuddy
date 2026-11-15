# BrokerBuddy

A private, self-hosted money dashboard. Import bank CSVs or PDF statements (or link a bank through Plaid), have transactions sorted into lender-style categories, see income and spending, and estimate how much you could borrow for a home.

## Run

```
go run .
```

Open **https://127.0.0.1:8443** (the certificate is self-signed, so accept the browser warning once; it is generated once and kept, so you won't be asked again). Then go to **Settings**.

**Everything is configured in the web interface.** There are no environment variables. The only startup options are optional flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `-db` | `brokerbuddy.db` | where your data lives |
| `-addr` | `127.0.0.1:8443` | local address for the web UI |

On first run an encryption key is generated next to the database (`brokerbuddy.db.key`). **Back up both files together**; without the key file, stored secrets (bank tokens, Plaid secret) can't be read.

## Settings

- **Banks:** optional Plaid keys (secret stored encrypted, never shown again), connected and imported accounts.
- **Categorisation:** rules, merchant lookups, re-run categorisation.
- **Security:** set a password. With no password the app answers only on this computer (it refuses other host names, which also blocks DNS-rebinding). Passwords are bcrypt-hashed, sign-in is rate limited, and cross-site form posts are rejected.
- **Access & HTTPS:** choose *This computer only*, *Internet with a free Let's Encrypt certificate* (needs a domain and open ports 80/443), or *your own certificate*. Changes apply immediately without a restart, and the local address always stays available so a wrong setting can't lock you out. Anything beyond this computer requires a password.

## Appearance

Use the **Auto / Light / Dark** control in the top bar. Auto follows your system setting. The choice is remembered per browser.

## Importing PDF statements

Upload a PDF on the **Import** page; the transaction table is found automatically and shown in the same mapping and preview screen as a CSV before anything is saved.

How it works: text and positions are read from every page and rebuilt into lines and cells; the table is found from its header row (even when the headings wrap over several lines), or, with no header, from rows that start with a date and end in amounts; numeric columns are located by clustering the amounts' x positions and labelled from the header; wrapped descriptions are re-joined; a date shown once per day carries to the rows below; dates without a year take it from the statement period; and money in/out comes from Debit/Credit columns, `CR`/`DR` markers, or the running balance (statements may list newest first). Opening/closing balances, totals, footers and repeated headers are dropped.

Limits: text-based PDFs only (a scan or photo has no text and needs OCR, which isn't included); password-protected files need the password entered on the upload form (it is used once and not stored); PDF 2.0 files and statements that combine several accounts with different column layouts in one file may not read correctly. Always check the preview, and use the CSV export when your bank offers one. Re-importing a statement is safe: duplicates are skipped.

## CSV import (no bank API needed)

Click **Import CSV**, upload an export from any bank. The app skips preamble lines, sniffs the delimiter, finds the header row, and guesses the date, description, amount (or separate debit/credit) columns and the date format; fix anything wrong with the dropdowns and check the preview. Mappings are remembered per header layout, so the next export from the same bank is one click. Re-importing overlapping exports skips duplicates. Amounts with a decimal comma (`4,50`) are not supported.

## Categories and the lender view

Transactions are bucketed into ~30 categories grouped the way an Australian lender reads statements: Income, Essential living, Discretionary, Debts & commitments, Risk flags (gambling, cash withdrawals, dishonours/fees, short-term credit) and Neutral (transfers, savings).

Classification order: your rules, then a built-in dictionary of AU merchants and bank phrases (longest phrase wins, whole words only, direction-aware so a salary credit is income but a debit is not), then cached online lookups, then the bank's own category.

**Learning from your edits:** every category you set by hand becomes an example. Similar transactions (same business name, ignoring store numbers and suburbs, tolerating typos like `ZORBA`/`ZORBAS`) follow automatically and are marked *learned*. An example that fully matches beats the built-in dictionary; a brand-only match (e.g. you categorised `COLES EXPRESS`, a new one says `COLES`) is used only where the dictionary has no answer, and is skipped if your examples for that brand disagree. Your explicit rules still win over everything, and a debit is never learned as income.

**Confidence ratings.** Every categorisation gets a score from 0 to 100, shown on each transaction (hover for the reason) and in the import preview: *confirmed* (you set it), *high* (80+), *medium* (50-79), *low* (1-49) or *unknown* (nothing matched). The classifier always makes an attempt, working down: your rules (95) > a merchant you categorised before (90) > the built-in list (66-90; a brand scores higher than a word like "cafe") > the bank's own category (50-55) > an online lookup (50) > a typo of a known brand (52) > similarity to other transactions in your history (up to 55, or 70 for an identical name) > wording cues such as "fee" or unexplained money in (20-45). Only when none of that applies is it left Uncategorized with a score of 0. Guesses are never used as evidence for further guesses.

**Fixing doubtful ones.** After an import you are taken straight to the *Needs review* list when anything is low or unknown. On Transactions, filter by confidence with the chips (each shows its count); the review list shows the least sure first with the same merchants together. Press ✓ to confirm a suggestion, or type a different category. Either way, the app then looks through your transaction history: other transactions from the same merchant (same name once store numbers, suburbs and states are ignored) take the category too, and anything close to it follows, unless you set it yourself. The page tells you how many were changed. If you categorised a merchant differently at different times, the most recent edit wins.

**Unknown merchants:** click *Look up unknown merchants*. It sends only cleaned business names (e.g. `bunnings`) to Wikipedia's free API, 12 per click, biggest spend first. Results are marked *lookup* on the Transactions tab, listed under Settings → Categorisation, and can be rejected. Wikipedia knows chains, not local shops: for those, set the category once and tick *rule*.

**Lender view** (`/report`): 3/6/12-month averages by group and category, % of income, surplus, and a list of things an assessor may notice. CSV download included. It is a preparation aid, not a serviceability calculation.

## Tabs

- **Dashboard** (`/`): income, spending, surplus and savings rate for 1/3/6/12 months or all time, monthly income-vs-expenses chart, top spending categories, recent transactions.
- **Transactions** (`/transactions`): the full list with search, category filter and inline category editing.
- **Charts** (`/charts`): filter by period, accounts, groups, categories, income/spending, search text, and day/week/month grouping. Seven views: income vs spending, stacked spending by category, category share (click a slice to filter), group split, top merchants, cumulative net, spending by weekday, plus the largest transactions. Filtering happens in the browser, so it is instant.
- **Mortgage** (`/mortgage`): see below.
- **Lender view** (`/report`), **Import** (`/import`) and **Settings** (`/settings`).

## Mortgage tab

1. Enter your deposit, state, buyer type (owner-occupier, first home buyer, investor), term, and either type a rate or pick one from the market list.
2. **Borrowing capacity** = your average monthly surplus (income minus spending from your transactions) that can service a loan at *rate + stress buffer* (default +3 points, like lenders use). Current rent is removed by default, since it stops when you buy. Existing loans, credit cards and BNPL stay in as spending.
3. **Purchase price** solves `price + stamp duty + other costs = deposit + loan`, with the loan capped by your maximum LVR.
4. **Ignore the current month** (tickbox): leaves this calendar month out of the income and spending averages, because it is only partly complete. The analysed window then ends at the last transaction before it. The choice is remembered.
5. **Sliders** change each spending category from -100% to +100% in 1% steps; the price recalculates as you drag.

**Rates.** *Search for current rates* downloads every home-loan rate that Australian banks, credit unions and building societies publish under the Consumer Data Right (free, no key; about 90 lenders, ~8,000 rates, ~1 minute). Staff, veteran, green-loan and other special-eligibility products and rate outliers are hidden unless you tick the box. Mortgage brokers don't publish a feed, so they can't be listed; they place loans with these same lenders.

**Stamp duty** schedules are built in for every state and territory, each with a status note saying how well it was checked (NSW is official 2026/27; several concessions changed in 2026 and are flagged *verify*). You can always type your own duty figure from the state's official calculator. First-home-buyer concession bands marked approximate use straight-line phase-ins.

**Not modelled:** lenders mortgage insurance, minimum living-expense benchmarks (HEM), income shading (e.g. 80% of rental income), HECS and credit-card limits. Treat the result as a planning aid.

## How it works

- Plaid Link (browser) → public token → server exchanges for an access token, stored AES-GCM encrypted in SQLite.
- `/transactions/sync` (cursor-based) pulls added/modified/removed transactions atomically.
- Categorisation priority is described above; manual edits survive re-syncs.

## Security notes

- Single-user app. See Settings → Security above for how access is controlled.
- Secrets in the database (bank access tokens, Plaid secret, private keys) are AES-GCM encrypted with the key file.
- Rate and merchant lookups send no personal data: only public rate requests, and cleaned business names for lookups you trigger.

## Test

```
go test ./...
```
