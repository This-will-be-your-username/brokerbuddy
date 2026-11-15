# FAQ and troubleshooting

## Does it connect to my bank?

Not directly. Australian banks have no free consumer API. Export a CSV or download a PDF statement and import it ([Importing data](Importing-Data)). Plaid linking exists for the regions Plaid covers (US, Canada, Europe) if you have keys.

## The browser warns about the certificate

The local certificate is self-signed. Accept it once; it is generated once and kept, so you are not asked again. Use [Let's Encrypt or your own certificate](Settings-and-Security#access-and-https) for a trusted one.

## "cross-origin request blocked" on a form

Form posts must come from the same address as the page. Open the app at one address only (for example `https://127.0.0.1:8443`, not a mix of `localhost` and `127.0.0.1`) and reload.

## My PDF says it could not find a transaction table

- It may be a scan or photo. Only text-based PDFs work; try the bank's CSV export.
- It may be password protected: enter the password on the upload form.
- Statements combining several accounts with different layouts in one file may fail. Import one account at a time.

## Everything went in with the wrong sign

Check the sign switch in the import preview (spending shown as negative or positive). For individual transactions, use **Make debit** or **Make credit** ([Transactions](Transactions#reversing-debit-and-credit)).

## Many transactions are Uncategorized

That is normal for small local businesses. Open **Needs review**, fix the biggest merchant once, and every transaction from it follows. Use **Suggest** for ideas, and **Look up unknown merchants** for chains ([Categorisation](Categorisation#fixing-things)).

## A category I set was changed

Categories you set by hand (shown as *confirmed*) are never changed by the app, including by reprocessing. If a row changed, it was not confirmed. A later correction of the same merchant also updates look-alikes you did not set yourself.

## Spending looks doubled

Flag your credit card account as a credit card ([Credit cards](Credit-Cards-and-Repayments)) so payments to the card are not counted a second time.

## The current month looks low

It is incomplete. On the Mortgage tab, tick **Ignore the current month**.

## Re-importing created duplicates

Re-imports are normally safe ([Importing data](Importing-Data#duplicates-and-re-importing)). If rows were imported by a very old version, run **Settings → Categorisation → Reprocess existing transactions** first, then re-import.

## No rates appear on the Mortgage tab

Click **Search for current rates**. It needs internet access and takes about a minute.

## I forgot my password

Stop the app, open the database with any SQLite tool and delete the `password_hash` row from the `settings` table, or restore a backup. Then set a new one. Back up first.

## Where is my data?

Next to the program: `brokerbuddy.db` and `brokerbuddy.db.key` (or the paths you gave with `-db`). Settings → Your data shows the exact location.

## Is this financial advice?

No. It is a planning aid. Lenders apply their own rules. See [Mortgage planner](Mortgage-Planner#what-is-not-modelled).
