# whatsapp-api

Four projects that together let an internal app send WhatsApp messages to employees safely.

```
your app  →  wa-laravel (SDK)  →  dispatcher  ═══►  baileys  →  WhatsApp
                                      ↑               ▲
                                      └── wa-console ──┘
                                       (operator dashboard)

═══►  sending. Only dispatcher may do this, and only on its own schedule.
───   operating. wa-console reads the queue through dispatcher and drives the session
      lifecycle — pair, restart, logout — on baileys directly, because none of that is
      a send and none of it is reachable through dispatcher.
```

The split is enforced, not agreed: baileys issues two scoped bearer tokens, and **the
console's token cannot send**. See [`dispatcher/ARCHITECTURE.md`](dispatcher/ARCHITECTURE.md).

## Running it

One compose file at the root runs the whole system. There are no per-project compose files.

```sh
cp .env.example .env    # then fill in the secrets it lists at the top
docker compose up -d --build
curl -s localhost:8080/v1/health
```

| Service | Where | What it is |
|---|---|---|
| `gateway` | http://localhost:3000 | baileys — talks to WhatsApp |
| `wa-consumer-api` | http://localhost:8080 | the queue's front door |
| `wa-dispatcher` | — | drains the queue, one send at a time per number |
| `asynqmon` | http://localhost:8081 | queue UI |
| `postgres` | `localhost:5433` | **shared** — one database each for baileys, dispatcher, console |
| `redis` | `localhost:6380` | **shared** — queue and slot allocator |

`wa-console` is not in the stack. It runs on the host and connects to the shared Postgres:

```sh
cd wa-console && composer run dev     # http://localhost:8000
```

**One `.env`, at the root.** Secrets shared between services — the gateway API key, the
admin key, the webhook secret — are defined once there instead of copied between projects,
which is how they used to drift apart and produce a 401 that looked like a gateway bug. The
`.env` inside each project is now only for running that project on the host (`npm run dev`,
`go test`, `php artisan`); containers never read them.

**One database server, three databases.** `baileys` owns `wa_sessions`/`wa_sent_messages`,
`dispatcher` owns `wa_jobs`, `console` owns users and the audit log. Each has its own owner
role, so no service can reach another's tables. `docker/postgres/init/01-databases.sh`
creates them on first boot and never runs again.

**Build files live in `docker/`.** One Dockerfile per service, built against that project's
directory as context. Only `.dockerignore` stays inside each project, because Docker reads
it from the build-context root.

**The Go services live under `go-consumer/`.** `wa-consumer-api`, `wa-dispatcher`, and the
`wa-shared` module they both depend on (via a `go.work` workspace) are grouped together at
`go-consumer/wa-consumer-api`, `go-consumer/wa-dispatcher`, and `go-consumer/wa-shared`.

---

## `baileys/` — the thing that actually talks to WhatsApp

*Node + TypeScript, Fastify, Postgres*

- **Multi-number** — connect several WhatsApp accounts at once in one service
- **QR + code pairing** — log in by scanning a QR or typing an 8-digit code on your phone
- **Send messages** — one HTTP call sends a message from a chosen number
- **Duplicate protection** — an idempotency key means the same message never goes out twice
- **Persistent login** — credentials live in Postgres, so restarts never cost a re-scan
- **Auto-reconnect** — drops are retried with backoff; a logged-out number stops instead of looping
- **Clear error codes** — every failure says what went wrong and whether it's worth retrying
- **Webhooks** — pushes inbound messages, delivery receipts, and status changes to your app, signed
- **Health + metrics** — per-number health plus Prometheus stats; flags a number as unhealthy after repeated failures
- **Docker-ready** — compose file, auto-applied database migrations, bearer-token auth

**In one line:** it's the phone — it sends and receives, nothing else. No queueing, scheduling, or retrying.

⚠️ Run exactly one copy. A WhatsApp session can only live in one process.

---

## `dispatcher/` — the thing that decides *when* to send

*Go, Redis (Asynq), Postgres*

- **Pacing** — spaces sends out so WhatsApp doesn't ban the number
- **One at a time per number** — never two sends in flight on the same account, ever
- **Priority lanes** — critical (OTPs) jumps the queue, bulk (digests) waits its turn
- **Coalescing** — several messages to the same person within a short window merge into one
- **Automatic retries** — failures back off based on the reason; hopeless ones stop immediately
- **Circuit breaker** — a sick number's queue pauses itself and resumes when it recovers
- **Message tracking** — look up any message's status, timeline, attempts, and last error
- **Batch sending** — up to 100 messages in one request
- **Dry run** — validate a real send that never reaches a phone
- **Webhooks out** — signed callbacks per API key, with retries, dead-letter, and replay
- **Operator API** — inspect jobs, retry, cancel, pause/resume a number, replay webhooks
- **Self-documenting** — guides + OpenAPI reference served from the binary at `/docs/`
- **API keys + metrics** — per-key access to senders, Prometheus stats, graded health endpoint

**In one line:** the traffic controller — everything you send goes into its queue and leaves on its schedule.

---

## `wa-laravel/` — the SDK your Laravel app installs

*PHP package (`wa/laravel`)*

- **Fluent sending** — `Wa::to(...)->text(...)->send()`, one line to queue a message
- **Notification channel** — send WhatsApp straight from a Laravel notification
- **Forced idempotency** — refuses to send without a key, so a retried job can't message someone twice
- **Typed exceptions** — one exception class per failure reason instead of parsing JSON
- **Priorities** — `->critical()` and `->bulk()` map to the dispatcher's lanes
- **Safe by default** — everything is a dry run outside production
- **Read back** — find a message, list/filter by your own reference, cancel, check sender health
- **Webhook handling** — signature verification plus events for sent / delivered / read / failed
- **Testing** — `Wa::fake()` so tests assert messages without hitting the network

**In one line:** ergonomics over the dispatcher API — nothing here that the raw API can't do.

---

## `wa-console/` — the operator dashboard

*Laravel 13, Livewire, Flux UI*

- **Overview** — live tiles for numbers, queue depth, and recent activity
- **Session management** — create, connect, pair by QR or code, restart, logout, reset a number
- **Live QR** — the pairing code refreshes on screen as WhatsApp rotates it
- **Message browser** — search, filter, and inspect any message's full timeline
- **Message actions** — cancel or retry a stuck message
- **Queue control** — pause and resume a number's queue by hand
- **Roles** — viewer, operator, and admin, each with its own set of allowed actions
- **Audit log** — every operator action recorded permanently and un-editable
- **Passkey login** — WebAuthn sign-in, plus password confirmation before destructive actions
- **Health banner** — warns you up front when the dispatcher or gateway is down
- **Runbook** — built-in page telling operators what to do when something breaks

**In one line:** the humans' window into the system — it watches and fixes, it never sends.
