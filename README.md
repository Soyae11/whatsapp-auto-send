# whatsapp-api

Three projects that together let an internal app send WhatsApp messages to employees safely.

```
                  wa-laravel (SDK)  →  wa-consumer-api  ┄┄►  wa-dispatcher  ═══►  gateway  →  WhatsApp
                       ▲                (queue front door)     (paces sends)       (baileys)
                       │                                                              ▲
                 wa-console: Send                                          wa-console: Sessions
              (single or batch, via a                                    (pair/logout/remove, via
               sender-scoped key)                                       the console-scoped key)

═══►  sending. Only wa-dispatcher may do this, and only on its own schedule.
┄┄►   queued — wa-consumer-api enqueues onto Redis, wa-dispatcher drains it.
```

The split is enforced, not agreed: baileys issues two scoped bearer tokens, and **the
console-scoped token cannot send** — a send that skipped it would also skip the pacing
that keeps a number from being banned. wa-console's own send feature goes through
`wa-laravel` like any other caller, with a `wa-consumer-api` key scoped to specific senders —
it has no shortcut into baileys. Queue operations (retry, cancel, pause/resume) still happen
through wa-consumer-api's `/internal/*` admin routes directly.

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
| `wa-console` | http://localhost:8000 | operator console — session pairing/logout/removal, and sending. Runs on the host, not in compose |
| `asynqmon` | http://localhost:8081 | queue UI |
| `postgres` | `localhost:5433` | **shared** — one database each for baileys, dispatcher, console |
| `redis` | `localhost:6380` | **shared** — queue and slot allocator |

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
- **Webhooks** — pushes inbound messages, delivery receipts, and status changes to your app, signed
- **Health + metrics** — per-number health plus Prometheus stats; flags a number as unhealthy after repeated failures

**In one line:** it's the phone — it sends and receives, nothing else. No queueing, scheduling, or retrying.

⚠️ Run exactly one copy. A WhatsApp session can only live in one process.

---

## `go-consumer/wa-consumer-api/` — the queue's front door

*Go, Redis (Asynq), Postgres*

- **Send messages** — one call queues a send (`POST /v1/messages`)
- **Batch sending** — up to 100 messages in one request
- **Coalescing** — several messages to the same person within a short window merge into one, at enqueue time
- **Forced idempotency** — a key is required to send; expired keys are purged hourly
- **Message tracking** — list/filter by sender, recipient, status, or reference, or look up any message's status, timeline, attempts, and last error
- **Cancel** — pull a still-queued message before it sends
- **Dry run** — validate a real send that never reaches a phone
- **Operator/admin API** (`/internal/*`) — inspect jobs, retry, cancel, pause/resume a number's queue, replay webhooks, inspect a session's circuit state
- **Delivery receipts in** — takes baileys' webhook events and turns them into its own webhook events out. Includes WhatsApp rejecting a message *after* accepting it (error 463, an unestablished-contact restriction) — the one case where `sent` would otherwise be a lie, since the gateway's own `/send` response had already returned success before the rejection arrived
- **API keys** — every request scoped to a key, senders included; provision one with
  `make create-key NAME=... PROJECT=... SENDERS=...` (`cmd/keys`)
- **Health endpoint** — reports gateway, database, and Redis reachability in one call

**In one line:** everything you send goes in here; it enqueues onto Redis and gets out of the way.

---

## `go-consumer/wa-dispatcher/` — the thing that decides *when* to send

*Go, Redis (Asynq), Postgres*

- **Pure background worker** — no HTTP port, drains the queue `wa-consumer-api` fills
- **Pacing** — spaces sends out so WhatsApp doesn't ban the number
- **One at a time per number** — a single asynq server per session at concurrency 1; never two sends in flight on the same account, ever
- **Priority lanes** — critical (OTPs) jumps the queue, bulk (digests) waits its turn, weighted 6:3:1
- **Automatic retries** — backoff tuned per failure reason, capped and jittered; hopeless ones stop immediately
- **Circuit breaker** — a sick session's queue pauses itself and resumes when it recovers, without being tripped by one bad recipient
- **Webhooks out** — delivers them itself, so a slow or dead webhook endpoint never adds latency to a send

**In one line:** the traffic controller — drains the queue one send at a time per number, on schedule.

---

## `wa-laravel/` — the SDK your Laravel app installs

*PHP package (`wa/laravel`)*

- **Fluent sending** — `Wa::to(...)->text(...)->send()`, one line to queue a message
- **Notification channel** — send WhatsApp straight from a Laravel notification
- **Forced idempotency** — refuses to send without a key, so a retried job can't message someone twice
- **Typed exceptions** — one exception class per failure reason instead of parsing JSON
- **Priorities** — `->critical()` and `->bulk()` map to wa-dispatcher's lanes
- **Safe by default** — everything is a dry run outside production
- **Read back** — find a message, list/filter by your own reference, cancel, check sender health
- **Webhook handling** — signature verification plus events for sent / delivered / read / failed
- **Testing** — `Wa::fake()` so tests assert messages without hitting the network

**In one line:** ergonomics over the wa-consumer-api — nothing here that the raw API can't do.

---

## `wa-console/` — the operator console

*Laravel 13, Inertia + React*

- **Pair by QR** — scan a code from WhatsApp's Linked Devices screen (polled, not live-streamed)
- **Pair by code** — type an 8-character code on the phone instead of scanning
- **Logout** — un-pairs the device
- **Remove** — deletes the session from baileys' database entirely, not just its credentials
- **Send** — single or batch, through `wa-laravel`; results (accepted/rejected) render per number
- **Messages** — search/filter by sender, recipient, status, or reference; inspect any
  message's full timeline and, when WhatsApp rejected it after accepting it, why

**In one line:** a thin, authenticated UI over baileys' session API and `wa-laravel` — it holds
no session data of its own, and its session-management credential can never send (sending goes
through a separate, sender-scoped `wa-consumer-api` key). Runs on the host (`composer run dev`),
not in Docker — see [`wa-console/README.md`](wa-console/README.md).
