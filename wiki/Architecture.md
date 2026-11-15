# Architecture

BrokerBuddy is a single Go program using only the standard library for the web layer, plus a pure Go SQLite driver (`modernc.org/sqlite`), `golang.org/x/crypto` (bcrypt, Let's Encrypt) and a PDF text reader (`github.com/ledongthuc/pdf`). Templates and static files are embedded with `go:embed`, so the binary is self-contained.

## Request flow

```
browser ──HTTPS──▶ net manager (self-signed / Let's Encrypt / custom cert)
                    └─▶ middleware: host check, login, same-origin check, headers
                         └─▶ handlers ──▶ SQLite (one connection)
                                     └─▶ html/template pages
```

The database uses a single connection, so code never runs a query while another result set is still open.

## Source layout

| File | Responsibility |
|------|----------------|
| `main.go` | Flags, startup, routes, category edits, transaction rows |
| `db.go` | Schema and in-place upgrades |
| `crypto.go` | AES-GCM vault and key file |
| `settings.go` | Settings page, password, sessions, rate limiting, security headers |
| `netserver.go`, `acme_staging.go` | HTTPS modes and certificates |
| `plaid.go`, `sync.go` | Optional Plaid linking and cursor sync |
| `csvimport.go`, `csvhandlers.go` | CSV parsing, column mapping, import and duplicate handling |
| `pdfimport.go`, `pdfheader.go`, `pdfstage4.go` | PDF text rebuilding and table detection |
| `taxonomy.go` | Categories, groups, built-in dictionary |
| `dict_more.go` | Extra generic words, brands, platforms |
| `clean.go` | Description cleaning, footer repair, legacy duplicate detection |
| `confidence.go` | The classification ladder and confidence scores |
| `learn.go` | Learning from your edits |
| `categorize.go` | Rules, caches, history, recategorisation, same-merchant fixes |
| `lookup.go`, `suggest.go` | Wikipedia lookups and suggestions |
| `reprocess.go` | Preview and apply for existing transactions |
| `cards.go` | Credit card ignoring and the debit/credit reversal |
| `reimb.go` | Repayments from other people |
| `txnfilter.go` | Transactions page, filters, paging |
| `dashboard.go`, `charts.go`, `report.go` | Analysis pages |
| `mortgage.go`, `duty.go`, `rates.go` | Mortgage planner, stamp duty, CDR rates |
| `demo.go` | Demo data |
| `templates/`, `static/` | Pages and the chart library |

## Data model

| Table | Holds |
|-------|-------|
| `accounts` | Linked or imported accounts, with a credit card flag |
| `transactions` | Amount (positive = money out), category, confidence, source, manual, ignored, repayment flags, `flipped` |
| `rules` | Your text rules |
| `merchant_cache`, `suggestion_cache` | Online lookup results |
| `csv_profiles` | Remembered column mappings per header layout |
| `mortgage_rates` | Downloaded CDR rates |
| `items` | Plaid items (tokens encrypted) |
| `settings` | Key and value settings (secrets encrypted) |

Transactions keep their original id. For imports it is a hash of account, date, amount and description (plus an occurrence number), which is what makes re-imports safe.

## Classification

See [Categorisation](Categorisation) for behaviour. In code, `classifyFull` runs the ladder in `confidence.go`: your rules, learned examples, the dictionary, bank category, cached lookups, typo and suffix guesses, history, cues. Then `reimbursement` decides whether a credit is a repayment from another person. `recategorize` applies the result to every transaction you have not confirmed, then `recomputeIgnored` works out which card repayments to ignore.

## Analysis

`buildReport` aggregates by category over a window and builds the groups, the income, spending and surplus, the notices, and the uncategorized line. The dashboard, Lender view and mortgage baseline all start from it, so they agree. `monthlySeries` builds the monthly chart with the same rules.

## Security design

See [Settings and security](Settings-and-Security).
