# curl collection

Runnable smoke tests for every endpoint this service exposes. Each script hits real routes,
prints the response, and asserts the status code — so `./run-all.sh` doubles as a
"is the deploy sane" check, not just a pile of copy-paste commands.

```sh
cd curl
./run-all.sh          # everything
./01-health.sh        # one script
```

Config comes from `../.env`, and anything already in your environment wins:

```sh
BASE_URL=http://staging:3000 ./01-health.sh
DISPATCHER_API_KEY=... CONSOLE_API_KEY=... BASE_URL=... ./run-all.sh
```

`jq` is used for pretty-printing and field assertions when installed, and skipped
gracefully when it isn't.

## Scripts

| Script | Covers | Needs |
|---|---|---|
| `01-health.sh` | `GET /health`, with and without a token | — |
| `02-auth.sh` | Bearer enforcement: missing, wrong, wrong-scheme, valid, query strings | — |
| `10-sessions.sh` | Create, list, detail, QR, 404s, payload rejection | — |
| `11-pair.sh` | Connect a socket and request a pairing code | `PHONE=` |
| `20-send.sh` | Send contract; real send and dedupe | `TO=` for the real send |
| `30-qr-stream.sh` | SSE headers, auth, and frames from the QR stream | — |
| `40-metrics.sh` | Prometheus exposition and the per-session health block | — |

`10-sessions.sh` writes the session it creates to `curl/.last-session`, so the later scripts
chain off it without you copying a uuid around. Override with `SESSION_ID=…`.

Scripts that would touch a real WhatsApp account refuse to run unless you name the number,
and report themselves as *skipped* rather than failed:

```sh
./10-sessions.sh                               # create a session
PHONE=6287713848500 ./11-pair.sh               # connect + pairing code
TO=6287713848500 ./20-send.sh                  # really sends a message
```

Webhooks have no curl script — they are outbound. Use `scripts/webhook-receiver.ts` to
verify signatures against a real consumer.

## Raw commands

If you just want to paste something into a terminal:

```sh
# The console credential: read + manage. Use DISPATCHER_API_KEY instead for /send.
export KEY=$(grep ^CONSOLE_API_KEY ../.env | cut -d= -f2)
export BASE=http://localhost:3000

# health — no auth needed
curl -s $BASE/health | jq

# any other route — bearer required
curl -s -H "Authorization: Bearer $KEY" $BASE/sessions | jq

# what an auth failure looks like
curl -s $BASE/sessions | jq
```

## The degraded path

`/health` returns 503 with `{"status":"degraded","db":"down"}` when Postgres is unreachable.
The process must stay up and recover on its own:

```sh
docker compose stop postgres
curl -s -i localhost:3000/health | head -1     # 503
docker inspect baileys-app-1 --format '{{.RestartCount}}'   # still 0
docker compose start postgres
curl -s localhost:3000/health | jq             # back to db: ok
```
