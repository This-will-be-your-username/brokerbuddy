# Getting started

## Install

The quickest way is a download for your platform from the repository's **Releases** page (Windows, macOS and Linux, Intel and ARM), or the Docker image. Step by step instructions for each are in [Installing and running](Installing-and-Running).

From source you need a recent [Go](https://go.dev/dl/) toolchain (the version is in `go.mod`) and a modern browser. The program is self-contained and uses a pure Go SQLite driver, so no C compiler or database server is needed.

## Run it

```
go run .
```

or, with a downloaded release, run `brokerbuddy` (`brokerbuddy.exe` on Windows). With Docker:

```
docker run -d -p 127.0.0.1:8443:8443 -v brokerbuddy-data:/data ghcr.io/<owner>/brokerbuddy:latest
```

Open **https://127.0.0.1:8443**. The certificate is self-signed and generated once, so your browser warns you the first time. Accept it once; it is kept between runs.

## Startup options

Everything else is configured in the web interface (**Settings**). There are no environment variables.

| Flag | Default | Meaning |
|------|---------|---------|
| `-db` | `brokerbuddy.db` | Path of the SQLite database |
| `-addr` | `127.0.0.1:8443` | Local address of the web interface |
| `-demo` | off | Fill an empty database with fictional [demo data](Demo-Data) |
| `-version` | | Print the version and exit |

## Your data files

| File | Contents |
|------|----------|
| `brokerbuddy.db` | Transactions, accounts, rules, settings |
| `brokerbuddy.db.key` | Encryption key for stored secrets |
| `certs` data (inside the database) | The self-signed or imported certificate |

**Back up the database and the key file together.** Without the key, saved secrets (a Plaid secret, bank tokens, private keys) cannot be read. Transactions themselves are in the database. Both files are listed in `.gitignore` so they are never committed.

## First steps

1. Open **Import** and upload a CSV or PDF statement ([Importing data](Importing-Data)).
2. Check the preview, choose the account, and import.
3. You land on the **Needs review** list if anything is uncertain. Fix a merchant once and all its transactions follow ([Categorisation](Categorisation)).
4. Tick any credit card accounts under **Settings → Banks → Credit cards** ([Credit cards](Credit-Cards-and-Repayments)).
5. Look at the **Dashboard**, **Charts** and **Mortgage** tabs.
6. Optionally set a password under **Settings → Security** ([Settings and security](Settings-and-Security)).

## Updating

Pull the new code and run again. The database is upgraded in place at startup, and your transactions are reprocessed with the newest logic. Your confirmed categories are never changed. Back up first if you want a safety net. See [reprocessing](Categorisation#reprocessing-existing-transactions).
