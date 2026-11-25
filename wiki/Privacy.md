# Privacy

BrokerBuddy is built so that your financial data stays on your computer.

## What is stored, and where

Everything is in one SQLite file and its key file, next to the program. Nothing is sent to a BrokerBuddy server, because there is none. There is no telemetry, analytics or account.

## What can leave your computer

Only things you trigger, and only these:

| Feature | What is sent | Where | When |
|---------|--------------|-------|------|
| Merchant lookup | A cleaned business name such as `bunnings` | Wikipedia's public API | You click *Look up unknown merchants* |
| Suggestions, search online | A cleaned business name | Wikipedia's public API | You click *Search online* on a transaction |
| Mortgage rates | Nothing personal: public requests for published rate data | Bank product APIs under the Consumer Data Right, and the CDR register | You click *Search for current rates* |
| Plaid (optional) | What Plaid needs to link your bank | Plaid | You set up Plaid |
| Telegram bot (optional) | A daily question with one transaction's description, date and amount, and your replies | Telegram | You turn the bot on in Settings. The merchant lookup it does still sends only the cleaned name |
| Let's Encrypt (optional) | Your domain and email | Let's Encrypt | You choose that access mode |

Lookups **never** send amounts, dates, account names or numbers, the full bank description, or people's names. The cleaned name strips store numbers, suburbs, states and payment wording first. Results are cached, so the same merchant is not searched twice.

The Chart.js library used for charts is served by the app itself (`static/`), so opening a page contacts no third party. The Plaid page loads Plaid's script only when you have set Plaid up.

## Keeping a repository clean

The `.gitignore` excludes databases, key files, certificates and `.env` files. The test data in the repository is synthetic, and the [demo data](Demo-Data) is fictional. Before publishing, see the checks in [Development and testing](Development-and-Testing#repository-hygiene).
