import { Boom } from '@hapi/boom'
import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPool, type Pool } from '../db.js'
import { ApiError } from '../errors.js'
import { createLogger } from '../logger.js'
import type { SessionManager } from '../sessions/manager.js'
import { createSession } from '../sessions/repository.js'
import { sendTextMessage, type SendRequest } from './send.js'
import { findByIdempotencyKey } from './repository.js'

const url = process.env.DATABASE_URL
const suite = url ? describe : describe.skip

suite('sendTextMessage', () => {
  const logger = createLogger('silent')
  let pool: Pool
  let sessionId: string
  let counter = 0

  /** Stands in for a live socket; the real one is never mocked for end-to-end tests. */
  function fakeSessions(overrides: {
    onWhatsApp?: () => Promise<{ jid: string; exists: boolean }[] | undefined>
    sendMessage?: () => Promise<{ key: { id: string } } | undefined>
    requireConnectedSocket?: () => never
  }): SessionManager {
    const socket = {
      onWhatsApp: overrides.onWhatsApp ?? (async () => [{ jid: '', exists: true }]),
      sendMessage: overrides.sendMessage ?? (async () => ({ key: { id: 'WA-1' } })),
    }
    return {
      // Health tracking is exercised in the manager's own tests; here it just must exist.
      recordSendOutcome: () => {},
      requireConnectedSocket:
        overrides.requireConnectedSocket ??
        (() => {
          socket.onWhatsApp = overrides.onWhatsApp ?? (async () => [{ jid: `${to}@s.whatsapp.net`, exists: true }])
          return socket
        }),
    } as unknown as SessionManager
  }

  const to = '6287713848500'
  const request = (overrides: Partial<SendRequest> = {}): SendRequest => ({
    idempotencyKey: `key-${counter}`,
    to,
    type: 'text',
    text: 'hello',
    ...overrides,
  })

  beforeAll(async () => {
    pool = createPool(url!, logger)
  })

  beforeEach(async () => {
    counter += 1
    sessionId = (await createSession(pool, `send test ${counter}`)).id
  })

  afterAll(async () => {
    await pool.query("DELETE FROM wa_sent_messages WHERE idempotency_key LIKE 'key-%'")
    await pool.query("DELETE FROM wa_sessions WHERE label LIKE 'send test %'")
    await pool.end()
  })

  const deps = (sessions: SessionManager) => ({ pool, sessions, logger })

  it('sends and records the attempt', async () => {
    const req = request()
    const result = await sendTextMessage(deps(fakeSessions({})), sessionId, req)

    expect(result).toMatchObject({ status: 'sent', waMessageId: 'WA-1', deduplicated: false })
    const row = await findByIdempotencyKey(pool, req.idempotencyKey)
    expect(row).toMatchObject({ status: 'sent', wa_message_id: 'WA-1', session_id: sessionId })
    expect(row?.payload).toEqual({ type: 'text', text: 'hello' })
  })

  it('returns the original result for a repeated key without sending again', async () => {
    const req = request()
    await sendTextMessage(deps(fakeSessions({})), sessionId, req)

    const sendMessage = vi.fn()
    const second = await sendTextMessage(deps(fakeSessions({ sendMessage })), sessionId, req)

    expect(second).toMatchObject({ deduplicated: true, waMessageId: 'WA-1' })
    expect(sendMessage).not.toHaveBeenCalled()
  })

  it('lets only one of two concurrent identical sends through', async () => {
    const req = request()
    let sends = 0
    const sessions = fakeSessions({
      sendMessage: async () => {
        sends += 1
        await new Promise((r) => setTimeout(r, 50))
        return { key: { id: `WA-${sends}` } }
      },
    })

    const results = await Promise.allSettled([
      sendTextMessage(deps(sessions), sessionId, req),
      sendTextMessage(deps(sessions), sessionId, req),
    ])

    // The second either loses the claim race (send_in_progress) or arrives after the first
    // finished (deduplicated). Either way WhatsApp is touched exactly once.
    expect(sends).toBe(1)
    const rejected = results.filter((r) => r.status === 'rejected')
    for (const r of rejected) {
      expect((r.reason as ApiError).errorCode).toBe('send_in_progress')
    }
  })

  it('rejects a number that is not on WhatsApp and records why', async () => {
    const req = request()
    const sessions = fakeSessions({ onWhatsApp: async () => [{ jid: '', exists: false }] })

    await expect(sendTextMessage(deps(sessions), sessionId, req)).rejects.toMatchObject({
      errorCode: 'not_on_whatsapp',
      statusCode: 422,
      retryable: false,
    })
    expect(await findByIdempotencyKey(pool, req.idempotencyKey)).toMatchObject({
      status: 'failed',
      error_code: 'not_on_whatsapp',
    })
  })

  it('treats an empty onWhatsApp response as not registered', async () => {
    const sessions = fakeSessions({ onWhatsApp: async () => undefined })
    await expect(sendTextMessage(deps(sessions), sessionId, request())).rejects.toMatchObject({
      errorCode: 'not_on_whatsapp',
    })
  })

  it('maps a disconnected socket to a retryable error and records it', async () => {
    const req = request()
    const sessions = fakeSessions({
      requireConnectedSocket: () => {
        throw new ApiError(409, 'session_not_connected', 'socket down', true)
      },
    })

    await expect(sendTextMessage(deps(sessions), sessionId, req)).rejects.toMatchObject({
      errorCode: 'session_not_connected',
      retryable: true,
    })
    expect(await findByIdempotencyKey(pool, req.idempotencyKey)).toMatchObject({
      status: 'failed',
      error_code: 'session_not_connected',
    })
  })

  it('maps a WhatsApp rate limit and stays retryable', async () => {
    const sessions = fakeSessions({
      sendMessage: async () => {
        throw new Boom('rate-overlimit', { statusCode: 429 })
      },
    })
    await expect(sendTextMessage(deps(sessions), sessionId, request())).rejects.toMatchObject({
      errorCode: 'rate_limited_by_wa',
      statusCode: 429,
      retryable: true,
    })
  })

  it('allows a retry after a failure, using the same key', async () => {
    const req = request()
    const failing = fakeSessions({
      sendMessage: async () => {
        throw new Boom('nope', { statusCode: 500 })
      },
    })
    await expect(sendTextMessage(deps(failing), sessionId, req)).rejects.toMatchObject({
      errorCode: 'send_failed',
    })

    // The worker owns retry policy; a previously failed key must not be permanently burned.
    const result = await sendTextMessage(deps(fakeSessions({})), sessionId, req)
    expect(result).toMatchObject({ status: 'sent', deduplicated: false })
    expect(await findByIdempotencyKey(pool, req.idempotencyKey)).toMatchObject({
      status: 'sent',
      error_code: null,
    })
  })


  it('returns the same canonical JID on a deduplicated replay as on the original send', async () => {
    const req = request()
    // WhatsApp can resolve to a JID that differs from the one we built from the input.
    const sessions = fakeSessions({
      onWhatsApp: async () => [{ jid: '6287713848500:7@s.whatsapp.net', exists: true }],
    })

    const first = await sendTextMessage(deps(sessions), sessionId, req)
    const second = await sendTextMessage(deps(sessions), sessionId, req)

    expect(first.to).toBe('6287713848500:7@s.whatsapp.net')
    // The regression: the replay used to return the locally-built JID from the claim row,
    // because markSent never wrote the resolved one back.
    expect(second).toMatchObject({ deduplicated: true, to: first.to })
  })

  it('times out a hung send instead of holding the request open', async () => {
    const sessions = fakeSessions({ sendMessage: () => new Promise(() => {}) })
    await expect(
      sendTextMessage(
        { ...deps(sessions), timeouts: { onWhatsApp: 5_000, sendMessage: 50 } },
        sessionId,
        request(),
      ),
    ).rejects.toMatchObject({ errorCode: 'upstream_timeout', retryable: true })
  })

  it('records a timeout as a failure so health tracking sees it', async () => {
    const req = request()
    const sessions = fakeSessions({ sendMessage: () => new Promise(() => {}) })
    await expect(
      sendTextMessage(
        { ...deps(sessions), timeouts: { onWhatsApp: 5_000, sendMessage: 50 } },
        sessionId,
        req,
      ),
    ).rejects.toThrow()
    expect(await findByIdempotencyKey(pool, req.idempotencyKey)).toMatchObject({
      status: 'failed',
      error_code: 'upstream_timeout',
    })
  })

  it('rejects an unusable number before claiming the key', async () => {
    const req = request({ to: '081234567890' })
    await expect(sendTextMessage(deps(fakeSessions({})), sessionId, req)).rejects.toMatchObject({
      errorCode: 'invalid_payload',
      statusCode: 400,
    })
    expect(await findByIdempotencyKey(pool, req.idempotencyKey)).toBeUndefined()
  })

  it('sends to the JID WhatsApp returned, not the one we built', async () => {
    const sendMessage = vi.fn(async () => ({ key: { id: 'WA-9' } }))
    const sessions = fakeSessions({
      onWhatsApp: async () => [{ jid: '6287713848500@s.whatsapp.net', exists: true }],
      sendMessage,
    })
    const result = await sendTextMessage(deps(sessions), sessionId, request())
    expect(result.to).toBe('6287713848500@s.whatsapp.net')
    expect(sendMessage).toHaveBeenCalled()
  })
})
