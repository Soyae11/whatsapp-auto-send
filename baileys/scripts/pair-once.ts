/**
 * Phase 1 acceptance harness — NOT the real session manager.
 *
 * Phase 1 has no user-facing output, so the only honest way to prove the Postgres auth
 * state works is to pair a throwaway number once and then reconnect from a cold process
 * with no QR and no pairing code. Phase 2 replaces this with `SessionManager` and the
 * `/sessions/*` endpoints; delete it then.
 *
 *   # first run — prints an 8-char pairing code to enter on the phone
 *   npm run pair -- my-session 6281234567890
 *
 *   # second run — must connect with no code at all
 *   npm run pair -- my-session
 *
 *   # pair by QR instead
 *   npm run pair -- my-session --qr
 *
 *   # start over — DESTROYS a working pairing, only use when creds are unusable
 *   npm run pair -- my-session 6281234567890 --fresh
 *
 * Deliberately absent: backoff, scheduling, status polling. Those are Phase 2's, and the
 * rules say none of them may leak in early. The one reconnect here is the 515 restart,
 * which is a required step of WhatsApp's pairing handshake rather than a retry policy.
 */
import { makeWASocket, delay, Browsers, DisconnectReason, fetchLatestBaileysVersion } from 'baileys'
import type { Boom } from '@hapi/boom'
import { loadMigrationConfig } from '../src/config.js'
import { createPool } from '../src/db.js'
import { createLogger } from '../src/logger.js'
import { ensureSession, setSessionStatus } from '../src/sessions/repository.js'
import {
  clearAuthState,
  isFullyPaired,
  isPairingIncomplete,
  usePostgresAuthState,
} from '../src/wa/auth-state.js'
import { InvalidPhoneNumberError, normalisePhoneNumber, toUserJid } from '../src/wa/phone.js'

const args = process.argv.slice(2)
const flags = new Set(args.filter((a) => a.startsWith('--')))
const [sessionIdArg, rawNumber] = args.filter((a) => !a.startsWith('--'))

const fresh = flags.has('--fresh')
const qrMode = flags.has('--qr')
const verbose = flags.has('--verbose')

if (!sessionIdArg) {
  console.error('usage: npm run pair -- <session-id> [phone-number] [--fresh] [--qr] [--verbose]')
  process.exit(1)
}
const sessionId: string = sessionIdArg

// Validate before touching the network. An unnoticed national-format number produces a
// pairing code that the phone can only reject with "check that the phone number is correct".
let phoneNumber: string | undefined
try {
  phoneNumber = rawNumber ? normalisePhoneNumber(rawNumber) : undefined
} catch (err) {
  if (err instanceof InvalidPhoneNumberError) {
    console.error(`\n✗ ${err.message}\n`)
    process.exit(1)
  }
  throw err
}

const config = loadMigrationConfig()
// --verbose turns on Baileys' own stanza-level logging. Its pairing failures are reported
// through the logger, not the return value, so this is how you see the real reason behind
// the phone's generic "Something went wrong".
const logger = createLogger(verbose ? 'debug' : config.LOG_LEVEL)
const pool = createPool(config.DATABASE_URL, logger)

async function die(code = 1): Promise<never> {
  await pool.end().catch(() => {})
  process.exit(code)
}

await ensureSession(pool, sessionId, `pair-once ${sessionId}`)
if (fresh) {
  await clearAuthState(pool, sessionId)
  await setSessionStatus(pool, sessionId, 'new')
  console.log(`\n[${sessionId}] --fresh: auth rows dropped, starting from scratch`)
}

const { state, saveCreds } = await usePostgresAuthState(pool, sessionId, logger)
const wasPairedAtStart = isFullyPaired(state.creds)

if (isPairingIncomplete(state.creds)) {
  console.error(
    `\n✗ [${sessionId}] has an incomplete pairing: a device JID but no account.\n` +
      `  The last attempt got as far as WhatsApp accepting it, then the socket died before\n` +
      `  linking finished — that is what the phone showed as "Something went wrong".\n` +
      `  These creds cannot log in. Start over:\n` +
      `      npm run pair -- ${sessionId} ${rawNumber ?? '<phone-number>'} --fresh\n`,
  )
  await die()
}

if (wasPairedAtStart) {
  console.log(`\n[${sessionId}] paired as ${state.creds.me?.id} — expecting a silent reconnect\n`)
} else if (!phoneNumber && !qrMode) {
  console.error(
    `\n✗ [${sessionId}] has no creds yet, so it needs to pair.\n` +
      `  Give a phone number in full international format, e.g.\n` +
      `      npm run pair -- ${sessionId} 6281234567890\n` +
      `  or pass --qr to pair by scanning instead.\n`,
  )
  await die()
} else {
  console.log(`\n[${sessionId}] no creds in Postgres — this run will pair`)
  if (phoneNumber) console.log(`  registering as: ${toUserJid(phoneNumber)}`)
}

const { version, isLatest } = await fetchLatestBaileysVersion()
console.log(`  WhatsApp web version: ${version.join('.')}${isLatest ? '' : ' (not latest)'}\n`)

const startedAt = Date.now()

// WhatsApp closes the socket with 515 immediately after a first-time pairing and expects
// the client to come straight back. Two allows for the pairing restart plus one spare.
const MAX_RESTARTS = 2
let restarts = 0

// Once we have our answer, ignore further connection events. Ending the socket ourselves
// emits a `close` that is not a failure, and Baileys' in-flight app-state sync will error
// on the way down — neither is worth reporting.
let finished = false

/** Time to let the initial app-state sync run, so the key store sees real Signal traffic. */
const SETTLE_MS = 8000

async function connect(): Promise<void> {
  // Recomputed per attempt: after pair-success the creds are complete, so the restarted
  // connection logs in instead of asking for another code.
  const pairedNow = isFullyPaired(state.creds)

  const sock = makeWASocket({
    version,
    auth: state,
    logger,
    // A stock browser tuple. The second element maps to the companion platform id WhatsApp
    // sees — invented values are not worth the risk during pairing.
    browser: Browsers.ubuntu('Chrome'),
    // The pairing window is the socket's lifetime: Baileys ends the connection once it runs
    // out of QR refs (60s for the first, then 20s each by default — often under two
    // minutes), and the code dies with it.
    qrTimeout: 120_000,
  })

  // Without this binding the creds Baileys mutates during pairing are never written, and the
  // next boot starts from scratch. It is the second most common cause of dead sessions.
  sock.ev.on('creds.update', saveCreds)

  let requestedCode = false

  async function requestCode(): Promise<void> {
    if (requestedCode || pairedNow || !phoneNumber || qrMode) return
    requestedCode = true

    // Small settle delay: the noise handshake has just finished and the server is still
    // setting up the registration attempt.
    await delay(2000)
    const code = await sock.requestPairingCode(phoneNumber)

    console.log(`\n  ┌────────────────────────┐`)
    console.log(`  │  pairing code: ${code}  │`)
    console.log(`  └────────────────────────┘\n`)
    console.log(`  Enter it on the phone whose WhatsApp account IS +${phoneNumber}:`)
    console.log(`    WhatsApp → Settings → Linked devices → Link a device`)
    console.log(`    → "Link with phone number instead" → enter the code above\n`)
    console.log(`  Keep this process running until it reports success — the code dies with`)
    console.log(`  the socket, and WhatsApp needs one more round trip after you type it.\n`)
  }

  sock.ev.on('connection.update', async (update) => {
    const { connection, qr, lastDisconnect } = update

    // A qr means the server is issuing pairing material, so the socket is ready.
    if (qr && !pairedNow) {
      if (qrMode) {
        // Dev convenience only. Phase 4 streams the raw string to the dashboard over SSE
        // and lets the browser render it, so this renderer stays a devDependency.
        const { default: qrcode } = await import('qrcode-terminal')
        console.log('\nScan with WhatsApp → Linked devices → Link a device:\n')
        qrcode.generate(qr, { small: true })
      } else {
        await requestCode()
      }
    }

    if (connection === 'open') {
      if (finished) return
      finished = true

      await setSessionStatus(pool, sessionId, 'connected')
      console.log(`\n✓ connected as ${sock.user?.id}`)
      console.log(
        wasPairedAtStart
          ? '✓ reconnected from Postgres auth state with no QR and no pairing code'
          : `✓ paired. Now run \`npm run pair -- ${sessionId}\` with no number — no --fresh —\n` +
              `  to prove it survives a restart.`,
      )

      // Staying up briefly lets Baileys run its initial app-state sync, which is the only
      // thing that exercises the key store with real Signal data. Tearing the socket down
      // the instant it opens leaves that half-done and logs a wall of harmless errors.
      console.log(`\n  letting the initial sync settle for ${SETTLE_MS / 1000}s…`)
      await delay(SETTLE_MS)

      const { rows } = await pool.query<{ key_type: string; n: string }>(
        'SELECT key_type, count(*)::text AS n FROM wa_auth_keys WHERE session_id = $1 GROUP BY key_type ORDER BY key_type',
        [sessionId],
      )
      if (rows.length > 0) {
        console.log(`\n  keys persisted to Postgres:`)
        for (const row of rows) console.log(`    ${row.key_type.padEnd(24)} ${row.n}`)
      } else {
        console.log(`\n  no keys written yet — normal if the account has no traffic to sync`)
      }
      console.log()

      await sock.end(undefined)
      await pool.end()
      process.exit(0)
    }

    if (connection === 'close') {
      if (finished) return
      const status = (lastDisconnect?.error as Boom | undefined)?.output?.statusCode

      if (status === DisconnectReason.loggedOut) {
        // Never reconnect after a logout — that is how a temporary restriction becomes
        // permanent. Phase 2 also clears the auth rows here.
        await setSessionStatus(pool, sessionId, 'logged_out')
        console.error(`\n✗ WhatsApp rejected these creds (401): ${lastDisconnect?.error?.message}`)

        // Pre-keys are only uploaded once a login completes. None means this session paired
        // but never finished — WhatsApp assigned the device and then discarded it, so the
        // creds point at a device that no longer exists.
        const { rows } = await pool.query<{ n: string }>(
          'SELECT count(*)::text AS n FROM wa_auth_keys WHERE session_id = $1',
          [sessionId],
        )
        if (rows[0]?.n === '0') {
          console.error(`  This session never completed a login — no keys were ever uploaded.`)
          console.error(`  The earlier pairing stopped at WhatsApp's 515 restart, so the device`)
          console.error(`  it assigned was dropped. Pair again; the restart is handled now:`)
          console.error(`      npm run pair -- ${sessionId} ${rawNumber ?? '<phone-number>'} --fresh\n`)
        } else {
          console.error(`  The device was unlinked from the phone. Pair again with --fresh.\n`)
        }
        await die()
      }

      // 515 is not a failure. WhatsApp always tears the socket down right after a first
      // pairing and requires a fresh connection to finish logging in and upload pre-keys.
      // Treating it as an error leaves a session that paired but never completed.
      if (status === DisconnectReason.restartRequired && restarts < MAX_RESTARTS) {
        restarts += 1
        console.log(`\n↻ WhatsApp asked for a restart (515) — the normal last step of pairing`)
        console.log(`  reconnecting (${restarts}/${MAX_RESTARTS})…`)
        await connect()
        return
      }

      const seconds = Math.round((Date.now() - startedAt) / 1000)
      const error = lastDisconnect?.error
      console.error(`\n✗ connection closed after ${seconds}s (status ${status ?? 'unknown'})`)
      if (error?.message) console.error(`  reason: ${error.message}`)
      if (verbose && error?.stack) console.error(error.stack)

      if (isPairingIncomplete(state.creds)) {
        console.error(`  pairing started but never completed — rerun with --fresh`)
      } else if (requestedCode) {
        console.error(`  the pairing code expired with the socket — rerun and type it faster`)
      }
      console.error('  this harness does not retry by design — just run it again\n')
      await die()
    }
  })
}

await connect()
