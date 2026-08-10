# baileys

The WhatsApp gateway. Holds sessions and sends messages when told to.

Part of the stack in the parent directory — see [`../README.md`](../README.md) for what it
does and how the pieces fit together.

## Run it

From the repo root, as part of the whole stack:

```sh
docker compose up -d --build
curl -s localhost:3000/health
```

Just this service:

```sh
docker compose up -d --build gateway
docker compose logs -f gateway
```

## Run it on the host

Needs the shared Postgres from the root stack (`docker compose up -d postgres`).

```sh
cp .env.example .env    # DATABASE_URL already points at localhost:5433
npm install
npm run migrate
npm run dev
```

| Command | What it does |
|---|---|
| `npm run dev` | Watch mode |
| `npm run build` / `npm start` | Compile to `dist/`, then run it |
| `npm run migrate` | Apply `migrations/` — needs only `DATABASE_URL` |
| `npm run typecheck` | `tsc --noEmit` |

## Test it

```sh
npm test                   # unit only — integration tests skip themselves
npm run test:integration   # against the shared Postgres on :5433
./curl/run-all.sh          # hits a running service; doubles as a deploy smoke test
```

## Pair a number

```sh
npm run pair -- my-session 62812xxxxxxx   # first run: prints an 8-character code
npm run pair -- my-session                # second run: connects with no code
```

Flags: `--fresh` drops the session's auth rows and starts over, `--qr` pairs by scanning,
`--verbose` turns on stanza-level logging.

Or through the API, which is what the console uses:

```sh
# connect and qr/stream are `manage` routes, so this is the console's credential.
KEY=$(grep ^GATEWAY_CONSOLE_KEY ../.env | cut -d= -f2)
curl -X POST -H "Authorization: Bearer $KEY" localhost:3000/sessions/$ID/connect
curl -N   -H "Authorization: Bearer $KEY" localhost:3000/sessions/$ID/qr/stream
```

## Two things that will bite you

**Run one replica.** A WhatsApp session is a singleton — two processes loading the same
auth state corrupt it.

**Baileys is pinned to an exact version** (`7.0.0-rc14`, no `^` or `~`). It is pre-1.0 and
the protocol surface moves between releases. Upgrade deliberately, never incidentally.
