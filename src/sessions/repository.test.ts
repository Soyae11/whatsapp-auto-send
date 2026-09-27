import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { createPool, type Pool } from '../db.js'
import { createLogger } from '../logger.js'
import {
  createSession,
  deleteSession,
  getSession,
  listRestorableSessions,
  setSessionConnected,
  setSessionStatus,
  type SessionStatus,
} from './repository.js'

const url = process.env.DATABASE_URL
const suite = url ? describe : describe.skip

suite('listRestorableSessions', () => {
  const logger = createLogger('silent')
  let pool: Pool

  beforeAll(async () => {
    pool = createPool(url!, logger)
  })

  beforeEach(async () => {
    await pool.query("DELETE FROM wa_sessions WHERE label LIKE 'restore test%'")
  })

  afterAll(async () => {
    await pool.query("DELETE FROM wa_sessions WHERE label LIKE 'restore test%'")
    await pool.end()
  })

  async function seed(status: SessionStatus): Promise<string> {
    const row = await createSession(pool, `restore test ${status}`)
    if (status !== 'new') await setSessionStatus(pool, row.id, status)
    return row.id
  }

  it('restores sessions that were connected or trying to connect', async () => {
    const connected = await seed('connected')
    const disconnected = await seed('disconnected')

    const ids = (await listRestorableSessions(pool)).map((r) => r.id)
    expect(ids).toContain(connected)
    expect(ids).toContain(disconnected)
  })

  it('leaves logged-out sessions alone', async () => {
    const loggedOut = await seed('logged_out')
    expect((await listRestorableSessions(pool)).map((r) => r.id)).not.toContain(loggedOut)
  })

  it('leaves never-paired sessions alone', async () => {
    const fresh = await seed('new')
    expect((await listRestorableSessions(pool)).map((r) => r.id)).not.toContain(fresh)
  })

  it('skips sessions still pairing', async () => {
    const pairing = await seed('pairing')
    expect((await listRestorableSessions(pool)).map((r) => r.id)).not.toContain(pairing)
  })

  it('returns every restorable session, not just the first', async () => {
    const seeded = [await seed('connected'), await seed('connected'), await seed('disconnected')]
    const found = (await listRestorableSessions(pool)).map((r) => r.id)
    expect(seeded.filter((id) => found.includes(id))).toHaveLength(3)
  })

  it('records the phone number when a session connects', async () => {
    const id = await seed('new')
    await setSessionConnected(pool, id, '6287713848500')
    const row = (await listRestorableSessions(pool)).find((r) => r.id === id)
    expect(row).toMatchObject({ status: 'connected', phone_number: '6287713848500' })
  })
})

suite('deleteSession', () => {
  const logger = createLogger('silent')
  let pool: Pool

  beforeAll(async () => {
    pool = createPool(url!, logger)
  })

  afterAll(async () => {
    await pool.query("DELETE FROM wa_sessions WHERE label LIKE 'delete test%'")
    await pool.end()
  })

  it('removes the row and cascades to its auth rows', async () => {
    const row = await createSession(pool, 'delete test cascade')
    await pool.query("INSERT INTO wa_auth_creds (session_id, creds) VALUES ($1, '{}'::jsonb)", [
      row.id,
    ])

    await deleteSession(pool, row.id)

    expect(await getSession(pool, row.id)).toBeUndefined()
    const creds = await pool.query('SELECT 1 FROM wa_auth_creds WHERE session_id = $1', [row.id])
    expect(creds.rowCount).toBe(0)
  })

  it('is a no-op for an unknown id', async () => {
    await expect(deleteSession(pool, '00000000-0000-0000-0000-000000000000')).resolves.not.toThrow()
  })
})
