# Demo data

Demo data lets you try BrokerBuddy, test changes, or take screenshots without loading any real bank data.

## What it contains

About 760 transactions over the last 12 months, ending today, across three accounts:

| Account | Type | Contents |
|---------|------|----------|
| Demo Everyday | Transaction account | Two salaries, rent, bills, insurance, subscriptions, groceries, fuel, a gym, a physio, ATM withdrawals, credit card payments, repayments from friends |
| Demo Savings | Savings | Monthly transfers in, interest |
| Demo Visa | Credit card | Cafes, takeaway, retail, a holiday, and a monthly "payment received" |

Everything is invented: people (Alex Sample, Jordan Example, Taylor Demo), suburbs (Exampleville, Sampleton), local shops, amounts. Well-known national brands (Woolworths, Netflix, Qantas) appear as examples. A few small local businesses are deliberately unknown to the built-in rules, so the review list, [suggestions](Categorisation#suggestions-for-doubtful-transactions) and the uncategorized notice have something to show.

The data is generated from a fixed random seed, so it is identical every time apart from the dates, which end on the day you load it.

## Loading it

**Separate database (recommended).** Keeps it away from anything real:

```
go run . -db demo.db -demo
```

**From the web interface.** On an empty dashboard click **Load demo data**, or use **Settings → Demo data**. Loading is refused if you already have transactions of your own, so demo and real data never mix.

## Removing it

**Settings → Demo data → Remove demo data** deletes only the demo accounts and their transactions. Your own data is not touched. Or delete the `demo.db` file if you used a separate database.

## How demo rows are identified

Demo accounts and transactions have ids starting with `demo_`. All demo features (load, remove, the "already loaded" check) use that prefix. See `demo.go`.

## Using it for tests and screenshots

The tests load the demo set into a temporary database to check that it produces a sensible dashboard. The screenshots in this wiki were taken from it with `scripts/screenshots.js` ([Development and testing](Development-and-Testing#screenshots)).
