# Settings and security

All configuration is done in the web interface under **Settings**. There are no environment variables and no config files. The only startup options are the [flags](Getting-Started#startup-options).

![Settings](images/settings.png)

## Settings sections

| Section | What it controls |
|---------|------------------|
| Banks | Optional Plaid keys, connected and imported accounts, credit card flags |
| Categorisation | Your rules, repayments from other people, merchant lookups, reprocessing |
| Demo data | Load or remove the [demo data](Demo-Data) |
| Telegram | Optional bot that asks about one uncategorised transaction a day, see [Telegram bot](#telegram-bot) |
| AI | Optional LLM the Telegram bot can ask for a category, see [AI](#ai-llm) |
| Password | Set or remove the password |
| Access & HTTPS | Who can reach the app and over what certificate |
| Your data | Where the database and key file are, how to back up |

## Password

With **no password**, the app answers only on your local network, so a headless install can be set up from another device. It refuses requests from public internet addresses, and requests whose Host header is not an IP address, `localhost` or a local-only name (a single-label name, or one ending in `.local`, `.lan`, `.home.arpa` or `.internal`), which also blocks DNS-rebinding attacks from other websites. Anyone on your network can open it until you set a password, so set one first.

With a password (8 or more characters):

- Passwords are stored as bcrypt hashes.
- Sign-in is rate limited per client address.
- Sessions use a cookie that is HTTP-only and secure.
- The extra listeners under Access and HTTPS (internet or your own certificate) **require** a password.

## Access and HTTPS

The app is always reachable at `https://127.0.0.1:8443` (or your `-addr`) on the same computer, with a self-signed certificate that is generated once and kept. Choose how else it can be reached:

| Mode | Use when | Needs |
|------|----------|-------|
| This computer only | Default | Nothing |
| Internet, with a free Let's Encrypt certificate | You want access from anywhere | A domain pointing at this computer, ports 80 and 443 reachable, a password |
| Network or internet, with my own certificate | You already have a certificate | Paste the certificate and private key (PEM), a password |

Changes apply immediately, with no restart. The local address always stays available, so a wrong setting cannot lock you out. A staging option for Let's Encrypt lets you test without hitting rate limits.

## Protection against cross-site requests

Every form post is checked: it must come from the same origin (the browser's `Sec-Fetch-Site` header, falling back to `Origin` or `Referer`). A request from another site is rejected with "cross-origin request blocked". The app sends `Referrer-Policy: same-origin`, so nothing leaks to other sites while same-site posts keep a real Origin.

## Secrets at rest

Secrets in the database (Plaid secret, bank access tokens, the private key of an imported certificate) are encrypted with AES-GCM. The key is generated on first run and stored next to the database as `<db>.key`. **Back up both files together.** Without the key, stored secrets cannot be read. Transactions are stored in plain SQLite, so protect the folder as you would any financial file (full-disk encryption is a good idea).

## Backups

Copy `brokerbuddy.db` and `brokerbuddy.db.key` while the app is stopped, or use SQLite's backup tooling. To move to another computer, copy both files and start the app with `-db` pointing at the copy.

You can also use **Settings → Backup & migration**: **Export database** saves everything (key included) as one file, and **Import a backup** on the other install replaces all of its data with that file. The import form has a **Migrate the Telegram bot from the backup** option. A bot can only run on one install at a time, so:

- leave it ticked when moving to the new computer, then stop the old install (or switch its bot off);
- untick it to keep the importing install's own Telegram settings (or none), for example when the old install keeps running with the bot.

## Removing everything

Stop the app and delete the database and key files. Nothing is stored anywhere else.

## Telegram bot

An optional bot lets you categorise from your phone, from anywhere, without exposing BrokerBuddy to the internet. It polls Telegram, so no ports or webhooks are needed, but BrokerBuddy must be running.

**Set up**

1. In Telegram, message **@BotFather**, send `/newbot` and copy the token it gives you.
2. Open a chat with your new bot and press **Start**. It replies with your chat ID.
3. In **Settings → Telegram**, paste the token, enter the chat ID as the recipient, pick the hour, tick **Bot on** and save. A test message confirms it works.

**What it does**

Each day, at the chosen hour (the computer's local time), the bot picks one uncategorised merchant: whichever stands out most by total value or by how often it appears. It skips merchants it asked about in the last 30 days. It searches for suggestions (your own history and rules, plus a Wikipedia search of the cleaned merchant name) and messages the recipient a numbered list with buttons.

Reply with a number, a category name (a part is enough, such as `dining`) or `skip`. The transaction is confirmed with that category, and the other uncategorised transactions from the same merchant follow. 

Reply `more` (or tap **More categories**) to see every category and pick one. Commands: `/next` asks about another one now, `/status` shows how many transactions still need a category, `/skip` and `/help`. **Ask me about one now** in Settings does the same as `/next`.

**Security**: the bot only answers the configured chat. Anyone else who messages it is ignored (apart from `/start`, which tells them their own chat ID). The token is stored encrypted. Telegram sees the question and your replies, including the description and amount, so use a private chat that only you can read. See [Privacy](Privacy).

## AI (LLM)

Optionally connect a large language model in **Settings → AI**. The Telegram bot's question then has an **Ask AI** button (or reply `ai`, or send `/ai`). The model is given the transaction and your list of categories and answers with the most likely category, how sure it is and a one-line reason. You get a **Use …** button for its answer, plus **More categories** and **Skip**: the AI never categorises anything by itself.

- **Anthropic (Claude)**: paste an API key. The model defaults to `claude-opus-5-5`; enter another model ID to change it.
- **OpenAI-compatible**: OpenAI, OpenRouter, or a local model. Enter the model name and, for anything other than OpenAI, the API address, such as `http://localhost:11434/v1` for Ollama or `http://localhost:1234/v1` for LM Studio. Local servers usually need no key.

### Web search

Banks often shorten merchant names, and a model may not know a local business. Turn on **Web search** and the AI gets to check the web first:

- **Brave Search API** or **Tavily**: paste that service's API key (both have free tiers). Before asking the model, BrokerBuddy searches for the cleaned merchant name (for example `blue door Australia`) and adds the top five results to the question.
- **SearXNG (self-hosted)**: the same, using your own SearXNG instance. Enter its address, such as `http://localhost:8888`, and enable the `json` format under `search.formats` in SearXNG's `settings.yml`.
- **Anthropic web search** (Claude only): Claude searches the web itself when it doesn't recognise the merchant, up to three searches per question. Anthropic charges for each search on top of the model's tokens.

If a search fails, the AI still answers without it and the Telegram reply says so. When the web was used, the reply says which search and how many results. The test question on saving also runs the search, so a bad key or address shows up straight away.

Saving with **Send a test question** ticked asks the model about a sample transaction so a wrong key, address or model shows up straight away. The API keys are stored encrypted.

**Privacy**: only when you press Ask AI, the provider receives that transaction's description, merchant, date and amount, plus your category names. A model running on your own network keeps all of that at home. Brave, Tavily and SearXNG receive only the cleaned merchant name, never the amount, date or full description. With Anthropic web search, Claude writes the search queries and is told to search for the business name only. See [Privacy](Privacy).
