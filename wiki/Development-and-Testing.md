# Development and testing

## Run the tests

```
go test ./...
```

The suite takes about ten seconds. It uses temporary databases and in-process test servers, and needs no network (online searches are replaced by a stub).

What is covered:

- Categorisation: the dictionary, confidence levels, learning, history, noise cleaning, suffix guesses, reprocessing.
- Import: CSV parsing and mapping, duplicates, re-importing under older wording, and PDF import against synthetic statements.
- Cards, repayments from others, reversing direction, suggestions, demo data.
- Reports and the mortgage maths (annuities, stamp duty, the solver).
- Security: cross-origin posts blocked, login and rate limiting, HTTPS settings.

## Synthetic PDF statements

`testdata/pdf` holds invented bank statements, each with an `*.expected.json` listing the transactions it contains. See `testdata/README.md` for the layouts. Regenerate with:

```
node testdata/gen_pdfs.js
node testdata/gen_pdfs_extra.js
```

They need Edge or Chrome installed (they print HTML to PDF).

## Trying the UI by hand

A test-only harness serves the app over plain HTTP so any browser can open it:

```
BB_BROWSER=300 BB_DB=/path/to/demo.db go test -run TestBrowserHarness .
```

Then open http://127.0.0.1:8765 for 300 seconds. (The real server is always HTTPS.)

## Screenshots

The images in this wiki are produced from the [demo data](Demo-Data) by `scripts/screenshots.js`, which drives headless Edge or Chrome over the DevTools protocol:

```
go run . -db demo.db -demo        # create the demo database, then stop it
BB_BROWSER=300 BB_DB=demo.db go test -run TestBrowserHarness .
node scripts/screenshots.js wiki/images
```

Pages are captured in light and dark, plus a phone-width dashboard. Every shot is clipped so that the bottom of the Settings page, which shows the database path on your computer, is not included.

## Publishing the wiki

The wiki pages live in `wiki/` so they are reviewed with the code. GitHub wikis are a separate repository (`<repo>.wiki.git`), so publish with:

```
scripts/publish-wiki.sh
```

It clones the wiki repository, copies `wiki/` into it and pushes. Run it when you are ready: it publishes. Create the first wiki page once on GitHub first, or the wiki repository does not exist yet.

## Repository hygiene

The repository must never contain real financial data or anything that identifies you. Before publishing:

1. Confirm `.gitignore` keeps out `*.db`, `*.key`, `certs/` and `.env`.
2. Use placeholder identities for commits (`git config user.name` and `user.email`).
3. Search the whole history, not just the working tree, for your name, email, suburb and the businesses you use:

   ```
   git log -p --all | grep -i -E 'your-name|your-suburb|you@example'
   ```

4. Keep test data synthetic. Use `Suburbton`, `Exampleville` and invented shops, never real local businesses.
5. Only generic words and national brands go in the built-in rules. Add local shops as rules in Settings, which stay in your database.
6. Take screenshots only from the demo data.

## Contributing a change

1. Write a test first where you can (`go test ./...`).
2. Run `gofmt -l .` and `go vet ./...`.
3. Keep commits small and described in the imperative.
