import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { createPool, type Pool } from '../db.js'
import { createLogger } from '../logger.js'
import { SessionManager } from './manager.js'
import { createSession, getSession, setSessionStatus } from './repository.js'

const url = process.env.DATABASE_URL
const suite = url ? describe : describe.skip

suite('session health tracking', () => {
  const logger = createLogger('silent')
  let pool: Pool
  let manager: SessionManager
  let sessionId: string

  beforeAll(async () => {
    pool = createPool(url!, logger)
  })

  beforeEach(async () => {
    sessionId = (await createSession(pool, 'health test')).id
    await setSessionStatus(pool, sessionId, 'connected')
    manager = new SessionManager(pool, logger)
    const entries = manager as unknown as { entries: Map<string, unknown> }
    entries.entries.set(sessionId, {
      id: sessionId,
      status: 'connected',
      qr: undefined,
      phoneNumber: '6287713848500',
      lastConnectedAt: new Date(),
      reconnectAttempts: 0,
      socket: {},
      restarts: 0,
      reconnectTimer: undefined,
      closing: false,
      pendingCreds: Promise.resolve(),
      lastSuccessfulSendAt: undefined,
      lastFailedSendAt: undefined,
      consecutiveFailures: 0,
      lastErrorCode: undefined,
      reconnectDueAt: undefined,
    })
  })

  afterAll(async () => {
    await pool.query("DELETE FROM wa_sessions WHERE label = 'health test'")
    await pool.end()
  })

  const fail = (errorCode: string) => manager.recordSendOutcome(sessionId, { ok: false, errorCode })
  const settle = () => new Promise((r) => setTimeout(r, 60))

  it('counts consecutive transport failures', () => {
    fail('send_failed')
    fail('send_failed')
    expect(manager.snapshot(sessionId)).toMatchObject({
      consecutiveFailures: 2,
      lastErrorCode: 'send_failed',
    })
  })

  it('flips to unhealthy at the threshold and persists it', async () => {
    for (let i = 0; i < 5; i += 1) fail('send_failed')
    await settle()

    expect(manager.snapshot(sessionId)?.status).toBe('unhealthy')
    // The Go worker reads this from the API, so it has to reach Postgres too.
    expect((await getSession(pool, sessionId))?.status).toBe('unhealthy')
  })

  it('does not flip on failures that are the caller\'s fault', async () => {
    for (let i = 0; i < 8; i += 1) fail('not_on_whatsapp')
    await settle()

    expect(manager.snapshot(sessionId)).toMatchObject({
      status: 'connected',
      consecutiveFailures: 0,
      lastErrorCode: 'not_on_whatsapp',
    })
  })

  it('counts the other session-fault codes too', () => {
    fail('session_not_connected')
    fail('rate_limited_by_wa')
    expect(manager.snapshot(sessionId)?.consecutiveFailures).toBe(2)
  })

  it('resets the counter on any success', () => {
    fail('send_failed')
    fail('send_failed')
    manager.recordSendOutcome(sessionId, { ok: true })

    expect(manager.snapshot(sessionId)).toMatchObject({
      consecutiveFailures: 0,
      lastErrorCode: undefined,
    })
    expect(manager.snapshot(sessionId)?.lastSuccessfulSendAt).toBeInstanceOf(Date)
  })

  it('recovers from unhealthy on the first success', async () => {
    for (let i = 0; i < 5; i += 1) fail('send_failed')
    await settle()
    expect(manager.snapshot(sessionId)?.status).toBe('unhealthy')

    manager.recordSendOutcome(sessionId, { ok: true })
    await settle()

    expect(manager.snapshot(sessionId)?.status).toBe('connected')
    expect((await getSession(pool, sessionId))?.status).toBe('connected')
  })

  it('still hands out a socket while unhealthy, or it could never recover', async () => {
    for (let i = 0; i < 5; i += 1) fail('send_failed')
    await settle()

    expect(() => manager.requireConnectedSocket(sessionId)).not.toThrow()
  })

  it('records the failure time without a success', () => {
    fail('send_failed')
    expect(manager.snapshot(sessionId)?.lastFailedSendAt).toBeInstanceOf(Date)
    expect(manager.snapshot(sessionId)?.lastSuccessfulSendAt).toBeUndefined()
  })

  it('ignores outcomes for sessions it does not hold', () => {
    expect(() => manager.recordSendOutcome('not-a-session', { ok: true })).not.toThrow()
  })
})
