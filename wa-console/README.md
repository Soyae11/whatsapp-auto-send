# wa-console

The operator console. Three features: WhatsApp session lifecycle (pair, logout, remove),
sending a message — single or batch — and browsing/inspecting any message's history, both
through [`wa-laravel`](../wa-laravel).

Part of the stack in the parent directory — see [`../README.md`](../README.md) for what it
does and how the pieces fit together.

*Laravel 13, Inertia + React*

**Sessions** — talks directly to baileys' HTTP API with the console-scoped credential
(`BAILEYS_CONSOLE_KEY`), which can read and manage sessions but can never send a message:

- **Pair by QR** — scan a code from WhatsApp's Linked Devices screen (polled, not live-streamed)
- **Pair by code** — type an 8-character code on the phone instead of scanning
- **Logout** — un-pairs the device
- **Remove** — deletes the session from baileys' database entirely, not just its credentials

It holds no session data of its own — baileys is the source of truth.

**Send** — a single page (session, numbers, message) that sends through `wa-laravel`, using a
`wa-consumer-api` key scoped to specific senders (never a raw session id). One number or many:
every send goes through `Wa::sendMany()`, so a single recipient is just a batch of one. Results
(accepted/rejected, per number) render inline — no polling needed, the request is synchronous.
This is the queueing outcome, not final delivery — see Messages for that.

**Messages** — search and filter every message this key has sent (by sender, recipient,
status, or reference), and open one to see its full timeline (queued → sending → sent →
delivered/read, or failed) plus the actual error when WhatsApp rejects a message after
accepting it. This is the only place that shows final delivery outcome, since Send only
reports whether a message was queued.

## Run it on the host

Needs the shared Postgres, gateway, and wa-consumer-api from the root stack:

```sh
docker compose up -d postgres gateway wa-consumer-api wa-dispatcher redis
```

Then, from this directory:

```sh
cp .env.example .env    # DB_* and BAILEYS_* already point at the root stack; fill in the rest
composer install
npm install
php artisan migrate
composer run dev
```

`WA_KEY` needs a real API key scoped to the senders this console should use — mint one with
(from `go-consumer/wa-consumer-api`):

```sh
make create-key NAME=wa-console PROJECT=internal SENDERS=<sender-a>,<sender-b>
```

`WA_DRY_RUN=false` is set deliberately: `wa-laravel` defaults to dry-run outside
`APP_ENV=production`, which would make the Send page a no-op here.

`composer run dev` runs the PHP server, queue listener, and Vite dev server together. The app
is at `http://localhost:8000`; log in through the starter kit's default auth (register a user
first — there's no seeded account).

| Command | What it does |
|---|---|
| `composer run dev` | App + queue + Vite, watching |
| `php artisan migrate` | Applies the framework's own tables (users, cache, jobs, sessions) — baileys owns the session data itself |
| `npm run build` | Production frontend build |
| `php artisan test` | Backend tests |
