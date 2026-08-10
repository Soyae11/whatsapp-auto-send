/**
 * Proves auth state survives a process restart, without needing WhatsApp.
 *
 * Phase 1's real acceptance test is `scripts/pair-once.ts` with a live number. This covers
 * the half that can be checked unattended: that everything Baileys would need is in
 * Postgres and nothing is hiding in process memory. It spawns two child processes — one
 * writes, one reads — and compares fingerprints, so a module-level cache or a pool that
 * happens to still be warm cannot make it pass.
 *
 *   DATABASE_URL=postgres://... npx tsx scripts/verify-auth-restart.ts
 */
import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { createHash } from 'node:crypto'
import { initAuthCreds } from 'baileys'
import { loadMigrationConfig } from '../src/config.js'
import { createPool } from '../src/db.js'
import { createLogger } from '../src/logger.js'
import { ensureSession } from '../src/sessions/repository.js'
import {
  clearAuthState,
  loadCreds,
  makePostgresSignalKeyStore,
  saveCreds,
} from '../src/wa/auth-state.js'
import { serialise } from '../src/wa/serialisation.js'

const SELF = fileURLToPath(import.meta.url)
const [mode = 'parent', sessionId = `verify-restart-${Date.now()}`] = process.argv.slice(2)

const config = loadMigrationConfig()
const logger = createLogger('silent')

/** Every byte Baileys would need, reduced to one comparable value. */
const fingerprint = (creds: unknown, keys: unknown): string =>
  createHash('sha256').update(serialise({ creds, keys })).digest('hex')

const KEY_IDS = ['1', '2', '3']

async function child(write: boolean): Promise<void> {
  const pool = createPool(config.DATABASE_URL, logger)
  const store = makePostgresSignalKeyStore(pool, sessionId)
  try {
    if (write) {
      await ensureSession(pool, sessionId, 'verify-auth-restart')
      const creds = initAuthCreds()
      creds.registered = true
      creds.me = { id: '62812000000:7@s.whatsapp.net', name: 'restart probe' }
      creds.routingInfo = Buffer.from([0x00, 0x7f, 0xff])
      await saveCreds(pool, sessionId, creds)
      await store.set({
        'pre-key': Object.fromEntries(
          KEY_IDS.map((id) => [
            id,
            { public: Buffer.from([Number(id), 0]), private: Buffer.from([0, Number(id)]) },
          ]),
        ),
        session: { 'a@s.whatsapp.net': Buffer.from([0xde, 0xad, 0xbe, 0xef]) },
      })
    }
    const creds = await loadCreds(pool, sessionId)
    const keys = {
      'pre-key': await store.get('pre-key', KEY_IDS),
      session: await store.get('session', ['a@s.whatsapp.net']),
    }
    process.stdout.write(fingerprint(creds, keys))
  } finally {
    await pool.end()
  }
}

function runChild(childMode: 'write' | 'read'): Promise<string> {
  return new Promise((resolve, reject) => {
    // Inherit execArgv so this works under tsx, and pass the session id along.
    const proc = spawn(process.execPath, [...process.execArgv, SELF, childMode, sessionId], {
      stdio: ['ignore', 'pipe', 'inherit'],
      env: process.env,
    })
    let out = ''
    proc.stdout.on('data', (chunk) => {
      out += String(chunk)
    })
    proc.on('error', reject)
    proc.on('exit', (code) =>
      code === 0 ? resolve(out.trim()) : reject(new Error(`${childMode} child exited ${code}`)),
    )
  })
}

if (mode === 'write' || mode === 'read') {
  await child(mode === 'write')
} else {
  console.log(`session: ${sessionId}\n`)

  const written = await runChild('write')
  console.log(`  pid A wrote  ${written}`)
  const read = await runChild('read')
  console.log(`  pid B read   ${read}\n`)

  const pool = createPool(config.DATABASE_URL, logger)
  try {
    await clearAuthState(pool, sessionId)
    await pool.query('DELETE FROM wa_sessions WHERE id = $1', [sessionId])
  } finally {
    await pool.end()
  }

  if (written === read) {
    console.log('✓ auth state is byte-identical across a process restart')
    process.exit(0)
  }
  console.error('✗ auth state changed across processes — a restart would force a re-pair')
  process.exit(1)
}
