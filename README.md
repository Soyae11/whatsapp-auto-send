# whatsapp-gateway

A small HTTP gateway to WhatsApp, built on [Baileys](https://github.com/WhiskeySockets/Baileys).
It keeps several WhatsApp numbers logged in, sends text messages from them, and pushes inbound
messages and delivery receipts to a webhook.

- **Multiple numbers** in one process
- **Pairing** by QR (live SSE stream) or an 8-character phone code
- **Persistent login**: auth state lives in Postgres, so a restart doesn't mean scanning the QR again
- **Idempotent sends**: the same `idempotencyKey` never sends twice
- **Auto-reconnect** with backoff. A logged-out number stops instead of retrying forever.
- **Signed webhooks** for `message.received`, `message.status` and `session.status_changed`
- **Health and Prometheus metrics**. A number is marked `unhealthy` after repeated send failures.

It doesn't queue, schedule or retry. If you send in bulk, pace the sends yourself.

## Run it

```sh
cp .env.example .env          # set API_KEY (openssl rand -hex 32)
docker compose up -d --build
curl -s localhost:3000/health
```

On the host, with your own Postgres:

```sh
cp .env.example .env          # point DATABASE_URL at it
npm install
npm run dev                   # applies migrations on boot
```

| Command | What it does |
|---|---|
| `npm run dev` | Watch mode, reads `.env` |
| `npm run build` / `npm start` | Compile to `dist/`, then run it |
| `npm run migrate` | Apply `migrations/` only |
| `npm run typecheck` | `tsc --noEmit` |
| `npm test` | Unit tests; the DB-backed tests skip themselves without `DATABASE_URL` |
| `npm run test:integration` | Every test, against `DATABASE_URL` |

## Configuration

| Variable | Required | |
|---|---|---|
| `API_KEY` | yes | Bearer token for every route except `/health`, at least 16 characters |
| `DATABASE_URL` | yes | Postgres connection string |
| `PORT` | yes | HTTP port |
| `LOG_LEVEL` | yes | `fatal` … `trace`, or `silent` |
| `WEBHOOK_URL` | no | Where events are POSTed. Leave empty to turn webhooks off |
| `WEBHOOK_SECRET` | with `WEBHOOK_URL` | HMAC key, at least 16 characters |
| `WEBHOOK_TIMEOUT_MS` | no | Default `5000` |

## API

Every route except `/health` needs `Authorization: Bearer $API_KEY`. Errors look like
`{ "error_code": "...", "message": "...", "retryable": true|false }`.

| Method | Path | |
|---|---|---|
| `GET` | `/health` | DB reachability and uptime, no auth |
| `GET` | `/metrics` | Prometheus text format |
| `POST` | `/sessions` | `{ "label": "..." }` → new session with `status: "new"` |
| `GET` | `/sessions` | All sessions with their live health |
| `GET` | `/sessions/:id` | One session |
| `POST` | `/sessions/:id/connect` | Open the socket. An unpaired session starts producing QR codes |
| `GET` | `/sessions/:id/qr` | Current QR, if any |
| `GET` | `/sessions/:id/qr/stream` | SSE events: `status`, `qr`, then `paired` or `failed` |
| `POST` | `/sessions/:id/pair` | `{ "phoneNumber": "62812..." }` → `{ "pairingCode": "ABCD1234" }` |
| `POST` | `/sessions/:id/send` | `{ "idempotencyKey", "to", "type": "text", "text" }` |
| `POST` | `/sessions/:id/restart` | Reconnect the socket |
| `POST` | `/sessions/:id/logout` | Log the number out |
| `POST` | `/sessions/:id/reset` | Wipe auth so the session can be paired again |
| `DELETE` | `/sessions/:id` | Remove the session and its auth |

### Pair a number and send

```sh
H="Authorization: Bearer $API_KEY"
ID=$(curl -s -H "$H" -H 'content-type: application/json' \
       -d '{"label":"main"}' localhost:3000/sessions | jq -r .id)

# Either scan the QR…
curl -s -X POST -H "$H" localhost:3000/sessions/$ID/connect
curl -N -H "$H" localhost:3000/sessions/$ID/qr/stream

# …or enter a code on the phone (WhatsApp → Linked devices → Link with phone number)
curl -s -H "$H" -H 'content-type: application/json' \
     -d '{"phoneNumber":"6281234567890"}' localhost:3000/sessions/$ID/pair

curl -s -H "$H" -H 'content-type: application/json' \
     -d '{"idempotencyKey":"order-42","to":"6281234567890","type":"text","text":"Hello"}' \
     localhost:3000/sessions/$ID/send
```

## Webhooks

Each event is POSTed as JSON with these headers:

- `x-baileys-event`: the event type
- `x-baileys-timestamp`: milliseconds since the epoch
- `x-baileys-signature`: `sha256=` + hex HMAC-SHA256 of `` `${timestamp}.${rawBody}` `` keyed with `WEBHOOK_SECRET`

To verify an event, recompute the signature over the raw body and compare the two in constant
time. Reject anything with an old timestamp. A delivery is attempted once, with no retries,
so a slow consumer never holds up message handling.

## Things to know

**Run one replica.** A WhatsApp session belongs to one process. Two processes that load the
same auth state corrupt it.

**Baileys is pinned to an exact version** (`7.0.0-rc14`). It's pre-1.0 and its protocol code
changes between releases, so only upgrade it on purpose.
