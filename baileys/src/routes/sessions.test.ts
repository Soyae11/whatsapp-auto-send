import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'
import type { AppInstance } from '../app.js'
import type { Config } from '../config.js'
import { createPool, type Pool } from '../db.js'
import { createLogger } from '../logger.js'
import { buildServer } from '../server.js'
import type { SessionManager, SessionSnapshot } from '../sessions/manager.js'

const url = process.env.DATABASE_URL
const suite = url ? describe : describe.skip

// Two credentials, because the gateway has two: the console manages sessions and reads,
// the dispatcher sends and reads. Neither can do the other's job, so these tests use
// whichever one the route under test actually requires.
const API_KEY = 'c'.repeat(32)
const SEND_KEY = 'k'.repeat(32)
const auth = { authorization: `Bearer ${API_KEY}` }
const sendAuth = { authorization: `Bearer ${SEND_KEY}` }

// Every "manage" route now requires wa-console to assert which of its own users a call is on
// behalf of — see routes/sessions.ts. These tests only care that ownership plumbing works, not
// about testing multiple distinct owners, so one constant stands in for "wa-console's caller".
const TEST_OWNER = 'route-test-owner'

function snap(overrides: Partial<SessionSnapshot> & { id: string }): SessionSnapshot {
  return {
    status: 'connected',
    qr: undefined,
    phoneNumber: undefined,
    lastConnectedAt: undefined,
    reconnectAttempts: 0,
    socketConnected: true,
    lastSuccessfulSendAt: undefined,
    lastFailedSendAt: undefined,
    consecutiveFailures: 0,
    lastErrorCode: undefined,
    nextReconnectInMs: undefined,
    ...overrides,
  }
}

suite('session routes', () => {
  const logger = createLogger('silent')
  let pool: Pool
  let app: AppInstance | undefined

  const config: Config = {
    DATABASE_URL: url ?? '',
    PORT: 0,
    LOG_LEVEL: 'silent',
    DISPATCHER_API_KEY: SEND_KEY,
    CONSOLE_API_KEY: API_KEY,
    WEBHOOK_TIMEOUT_MS: 5_000,
  }

  function makeApp(
    sessions: Partial<SessionManager> & { pairingCode?: () => Promise<string> } = {},
  ): AppInstance {
    const stub = {
      snapshot: () => undefined,
      connect: async (id: string) => snap({ id, status: 'pairing' }),
      restart: async (id: string) => snap({ id, status: 'disconnected' }),
      logout: async () => {},
      reset: async () => {},
      delete: async () => {},
      waitForPairingReady: async () => {},
      recordSendOutcome: () => {},
      requirePairingSocket: () => ({
        requestPairingCode: sessions.pairingCode ?? (async () => 'ABCD1234'),
      }),
      requireConnectedSocket: () => ({
        onWhatsApp: async () => [{ jid: '6287713848500@s.whatsapp.net', exists: true }],
        sendMessage: async () => ({ key: { id: 'WA-ROUTE-1' } }),
      }),
      ...sessions,
    } as unknown as SessionManager
    return buildServer({ config, pool, logger, sessions: stub })
  }

  async function createSessionVia(app: AppInstance, label = 'route test'): Promise<string> {
    const res = await app.inject({
      method: 'POST',
      url: '/sessions',
      headers: auth,
      payload: { label, ownerId: TEST_OWNER },
    })
    expect(res.statusCode).toBe(201)
    return res.json().id
  }

  beforeAll(async () => {
    pool = createPool(url!, logger)
  })

  afterEach(async () => {
    await app?.close()
    app = undefined
  })

  afterAll(async () => {
    await pool.query("DELETE FROM wa_sent_messages WHERE idempotency_key LIKE 'route-%'")
    await pool.query("DELETE FROM wa_sessions WHERE label LIKE 'route test%'")
    await pool.end()
  })

  describe('POST /sessions', () => {
    it('creates a session with status new', async () => {
      app = makeApp()
      const res = await app.inject({
        method: 'POST',
        url: '/sessions',
        headers: auth,
        payload: { label: 'route test create', ownerId: TEST_OWNER },
      })
      expect(res.statusCode).toBe(201)
      expect(res.json()).toMatchObject({ label: 'route test create', status: 'new', hasQr: false })
      expect(res.json().id).toMatch(/^[0-9a-f-]{36}$/)
    })

    it('rejects a missing label with the error contract', async () => {
      app = makeApp()
      const res = await app.inject({
        method: 'POST',
        url: '/sessions',
        headers: auth,
        payload: { ownerId: TEST_OWNER },
      })
      expect(res.statusCode).toBe(400)
      expect(res.json()).toMatchObject({ error_code: 'invalid_payload', retryable: false })
    })

    it('rejects a missing ownerId with the error contract', async () => {
      app = makeApp()
      const res = await app.inject({
        method: 'POST',
        url: '/sessions',
        headers: auth,
        payload: { label: 'route test no owner' },
      })
      expect(res.statusCode).toBe(400)
      expect(res.json()).toMatchObject({ error_code: 'invalid_payload', retryable: false })
    })

    it('requires a bearer token', async () => {
      app = makeApp()
      const res = await app.inject({ method: 'POST', url: '/sessions', payload: { label: 'x' } })
      expect(res.statusCode).toBe(401)
    })
  })

  describe('GET /sessions', () => {
    it('lists sessions and prefers live status over the stored row', async () => {
      app = makeApp()
      const id = await createSessionVia(app, 'route test list')
      await app.close()

      // The row still says 'new'; the manager knows the socket is up.
      app = makeApp({
        snapshot: () => snap({ id, phoneNumber: '6287713848500', lastConnectedAt: new Date() }),
      })
      const res = await app.inject({ method: 'GET', url: '/sessions', headers: auth })
      expect(res.statusCode).toBe(200)
      const found = res.json().sessions.find((s: { id: string }) => s.id === id)
      expect(found).toMatchObject({ status: 'connected', phoneNumber: '6287713848500' })
    })
  })

  describe('multiple sessions', () => {
    it('reports each session independently rather than sharing one state', async () => {
      app = makeApp()
      const a = await createSessionVia(app, 'route test multi a')
      const b = await createSessionVia(app, 'route test multi b')
      const c = await createSessionVia(app, 'route test multi c')
      await app.close()

      const live: Record<string, { status: 'connected' | 'pairing'; qr?: string; phone?: string }> =
        {
          [a]: { status: 'connected', phone: '6281111111111' },
          [b]: { status: 'pairing', qr: 'qr-for-b' },
          [c]: { status: 'connected', phone: '6283333333333' },
        }
      app = makeApp({
        snapshot: (id: string) => {
          const entry = live[id]
          if (!entry) return undefined
          return snap({ id, status: entry.status, qr: entry.qr, phoneNumber: entry.phone })
        },
      })

      const list = (await app.inject({ method: 'GET', url: '/sessions', headers: auth })).json()
      const byId = Object.fromEntries(
        list.sessions.map((s: { id: string }) => [s.id, s]),
      ) as Record<string, { status: string; phoneNumber: string | null; hasQr: boolean }>

      expect(byId[a]).toMatchObject({ status: 'connected', phoneNumber: '6281111111111', hasQr: false })
      expect(byId[b]).toMatchObject({ status: 'pairing', hasQr: true })
      expect(byId[c]).toMatchObject({ status: 'connected', phoneNumber: '6283333333333' })

      const detailB = (await app.inject({ method: 'GET', url: `/sessions/${b}`, headers: auth })).json()
      expect(detailB.status).toBe('pairing')
      const qrB = (
        await app.inject({ method: 'GET', url: `/sessions/${b}/qr?ownerId=${TEST_OWNER}`, headers: auth })
      ).json()
      expect(qrB.qr).toBe('qr-for-b')
      const qrA = (
        await app.inject({ method: 'GET', url: `/sessions/${a}/qr?ownerId=${TEST_OWNER}`, headers: auth })
      ).json()
      expect(qrA.qr).toBeNull()
    })
  })

  describe('GET /sessions/:id', () => {
    it('returns detail', async () => {
      app = makeApp()
      const id = await createSessionVia(app)
      const res = await app.inject({ method: 'GET', url: `/sessions/${id}`, headers: auth })
      expect(res.statusCode).toBe(200)
      expect(res.json().id).toBe(id)
    })

    it('404s an unknown session with a stable code', async () => {
      app = makeApp()
      const res = await app.inject({
        method: 'GET',
        url: '/sessions/00000000-0000-0000-0000-000000000000',
        headers: auth,
      })
      expect(res.statusCode).toBe(404)
      expect(res.json()).toMatchObject({ error_code: 'session_not_found', retryable: false })
    })
  })

  describe('POST /sessions/:id/pair', () => {
    it('returns an 8-character pairing code', async () => {
      app = makeApp()
      const id = await createSessionVia(app)
      const res = await app.inject({
        method: 'POST',
        url: `/sessions/${id}/pair`,
        headers: auth,
        payload: { phoneNumber: '+62 877-1384-8500', ownerId: TEST_OWNER },
      })
      expect(res.statusCode).toBe(200)
      expect(res.json()).toMatchObject({ phoneNumber: '6287713848500', pairingCode: 'ABCD1234' })
      expect(res.json().pairingCode).toHaveLength(8)
    })

    it('rejects a national-format number before touching the socket', async () => {
      app = makeApp()
      const id = await createSessionVia(app)
      const res = await app.inject({
        method: 'POST',
        url: `/sessions/${id}/pair`,
        headers: auth,
        payload: { phoneNumber: '081234567890', ownerId: TEST_OWNER },
      })
      expect(res.statusCode).toBe(400)
      expect(res.json().error_code).toBe('invalid_payload')
      expect(res.json().message).toMatch(/leading zero/)
    })
  })

  describe('GET /sessions/:id/qr', () => {
    it('returns null when there is no current code', async () => {
      app = makeApp()
      const id = await createSessionVia(app)
      const res = await app.inject({
        method: 'GET',
        url: `/sessions/${id}/qr?ownerId=${TEST_OWNER}`,
        headers: auth,
      })
      expect(res.statusCode).toBe(200)
      expect(res.json()).toMatchObject({ id, qr: null })
    })

    it('returns the current code as a string', async () => {
      app = makeApp()
      const id = await createSessionVia(app)
      await app.close()
      app = makeApp({
        snapshot: () => snap({ id, status: 'pairing', qr: '2@abc,def,ghi' }),
      })
      const res = await app.inject({
        method: 'GET',
        url: `/sessions/${id}/qr?ownerId=${TEST_OWNER}`,
        headers: auth,
      })
      expect(res.json()).toMatchObject({ qr: '2@abc,def,ghi', status: 'pairing' })
    })
  })

  describe('POST /sessions/:id/send', () => {
    it('sends and returns the WhatsApp message id', async () => {
      app = makeApp()
      const id = await createSessionVia(app)
      const res = await app.inject({
        method: 'POST',
        url: `/sessions/${id}/send`,
        headers: sendAuth,
        payload: {
          idempotencyKey: `route-${Date.now()}`,
          to: '6287713848500',
          type: 'text',
          text: 'hi',
        },
      })
      expect(res.statusCode).toBe(200)
      expect(res.json()).toMatchObject({
        status: 'sent',
        waMessageId: 'WA-ROUTE-1',
        deduplicated: false,
      })
    })

    it('returns deduplicated: true for a repeated key', async () => {
      app = makeApp()
      const id = await createSessionVia(app)
      const payload = {
        idempotencyKey: `route-dedup-${Date.now()}`,
        to: '6287713848500',
        type: 'text',
        text: 'hi',
      }
      const url = `/sessions/${id}/send`
      await app.inject({ method: 'POST', url, headers: sendAuth, payload })
      const second = await app.inject({ method: 'POST', url, headers: sendAuth, payload })

      expect(second.statusCode).toBe(200)
      expect(second.json()).toMatchObject({ deduplicated: true, waMessageId: 'WA-ROUTE-1' })
    })

    it('returns 409 session_not_connected when the socket is down', async () => {
      const { sessionNotConnected } = await import('../errors.js')
      app = makeApp({
        requireConnectedSocket: () => {
          throw sessionNotConnected('x', 'disconnected')
        },
      })
      const id = await createSessionVia(app)
      const res = await app.inject({
        method: 'POST',
        url: `/sessions/${id}/send`,
        headers: sendAuth,
        payload: {
          idempotencyKey: `route-down-${Date.now()}`,
          to: '6287713848500',
          type: 'text',
          text: 'hi',
        },
      })
      expect(res.statusCode).toBe(409)
      expect(res.json()).toMatchObject({ error_code: 'session_not_connected', retryable: true })
    })

    it('returns 422 not_on_whatsapp as a permanent failure', async () => {
      app = makeApp({
        requireConnectedSocket: () =>
          ({ onWhatsApp: async () => [{ jid: '', exists: false }] }) as never,
      })
      const id = await createSessionVia(app)
      const res = await app.inject({
        method: 'POST',
        url: `/sessions/${id}/send`,
        headers: sendAuth,
        payload: {
          idempotencyKey: `route-nowa-${Date.now()}`,
          to: '6287713848500',
          type: 'text',
          text: 'hi',
        },
      })
      expect(res.statusCode).toBe(422)
      expect(res.json()).toMatchObject({ error_code: 'not_on_whatsapp', retryable: false })
    })

    it.each([
      ['missing idempotencyKey', { to: '6287713848500', type: 'text', text: 'hi' }],
      ['missing text', { idempotencyKey: 'route-x', to: '6287713848500', type: 'text' }],
      ['unsupported type', { idempotencyKey: 'route-x', to: '62877', type: 'image', text: 'hi' }],
      ['empty text', { idempotencyKey: 'route-x', to: '6287713848500', type: 'text', text: '' }],
    ])('rejects %s with invalid_payload', async (_name, payload) => {
      app = makeApp()
      const id = await createSessionVia(app)
      const res = await app.inject({
        method: 'POST',
        url: `/sessions/${id}/send`,
        headers: sendAuth,
        payload,
      })
      expect(res.statusCode).toBe(400)
      expect(res.json().error_code).toBe('invalid_payload')
    })

    it('404s before sending when the session does not exist', async () => {
      app = makeApp()
      const res = await app.inject({
        method: 'POST',
        url: '/sessions/00000000-0000-0000-0000-000000000000/send',
        headers: sendAuth,
        payload: {
          idempotencyKey: `route-missing-${Date.now()}`,
          to: '6287713848500',
          type: 'text',
          text: 'hi',
        },
      })
      expect(res.statusCode).toBe(404)
      expect(res.json().error_code).toBe('session_not_found')
    })
  })


  describe('GET /metrics', () => {
    it('exposes Prometheus text with per-session state', async () => {
      app = makeApp()
      const id = await createSessionVia(app, 'route test metrics')

      const res = await app.inject({ method: 'GET', url: '/metrics', headers: auth })
      expect(res.statusCode).toBe(200)
      expect(res.headers['content-type']).toContain('text/plain')

      const body = res.body
      expect(body).toContain('# TYPE baileys_uptime_seconds gauge')
      expect(body).toContain('baileys_sessions_total')
      expect(body).toContain(`baileys_session_up{session_id="${id}"`)
      expect(body).toContain(`status="unhealthy"`)
      expect(body).toContain(`baileys_session_consecutive_send_failures{session_id="${id}"`)
      expect(body.endsWith('\n')).toBe(true)
    })

    it('requires a bearer token like every other route', async () => {
      app = makeApp()
      const res = await app.inject({ method: 'GET', url: '/metrics' })
      expect(res.statusCode).toBe(401)
    })

    it('never puts phone numbers in labels', async () => {
      app = makeApp({
        snapshot: (id: string) => snap({ id, phoneNumber: '6287713848500' }),
      })
      await createSessionVia(app, 'route test metrics pii')
      const res = await app.inject({ method: 'GET', url: '/metrics', headers: auth })
      expect(res.body).not.toContain('6287713848500')
    })
  })

  describe('POST /sessions/:id/restart', () => {
    it('rebuilds the socket and reports the new status', async () => {
      app = makeApp()
      const id = await createSessionVia(app, 'route test restart')
      const res = await app.inject({ method: 'POST', url: `/sessions/${id}/restart`, headers: auth })
      expect(res.statusCode).toBe(200)
      expect(res.json()).toMatchObject({ id, status: 'disconnected' })
    })

    it('404s an unknown session', async () => {
      app = makeApp()
      const res = await app.inject({
        method: 'POST',
        url: '/sessions/00000000-0000-0000-0000-000000000000/restart',
        headers: auth,
      })
      expect(res.statusCode).toBe(404)
    })
  })

  describe('session health in GET /sessions/:id', () => {
    it('reports the fields the worker paces on', async () => {
      const lastConnected = new Date()
      app = makeApp({
        snapshot: (id: string) =>
          snap({
            id,
            status: 'unhealthy',
            lastConnectedAt: lastConnected,
            consecutiveFailures: 5,
            lastErrorCode: 'send_failed',
            reconnectAttempts: 2,
            nextReconnectInMs: 8000,
          }),
      })
      const id = await createSessionVia(app, 'route test health')
      const body = (await app.inject({ method: 'GET', url: `/sessions/${id}`, headers: auth })).json()

      expect(body.status).toBe('unhealthy')
      expect(body.health).toMatchObject({
        socketConnected: true,
        lastConnectedAt: lastConnected.toISOString(),
        consecutiveFailures: 5,
        lastErrorCode: 'send_failed',
        reconnectAttempts: 2,
        nextReconnectInMs: 8000,
      })
    })

    it('reports zeroed health for a session with no live socket', async () => {
      app = makeApp({ snapshot: () => undefined })
      const id = await createSessionVia(app, 'route test health idle')
      const body = (await app.inject({ method: 'GET', url: `/sessions/${id}`, headers: auth })).json()
      expect(body.health).toMatchObject({
        socketConnected: false,
        consecutiveFailures: 0,
        lastErrorCode: null,
        nextReconnectInMs: null,
      })
    })
  })


  describe('error contract regressions', () => {
    it('maps a Baileys Boom from pairing to a real code, not a bare 500', async () => {
      const { Boom } = await import('@hapi/boom')
      app = makeApp({
        pairingCode: async () => {
          throw new Boom('Connection Closed', { statusCode: 428 })
        },
      })
      const id = await createSessionVia(app, 'route test pair boom')
      const res = await app.inject({
        method: 'POST',
        url: `/sessions/${id}/pair`,
        headers: auth,
        payload: { phoneNumber: '6287713848500', ownerId: TEST_OWNER },
      })

      expect(res.statusCode).not.toBe(500)
      expect(res.json()).toMatchObject({ error_code: 'session_not_connected', retryable: true })
    })

    it('reports a logged-out device from pairing as permanent', async () => {
      const { Boom } = await import('@hapi/boom')
      app = makeApp({
        pairingCode: async () => {
          throw new Boom('Connection Failure', { statusCode: 401 })
        },
      })
      const id = await createSessionVia(app, 'route test pair 401')
      const res = await app.inject({
        method: 'POST',
        url: `/sessions/${id}/pair`,
        headers: auth,
        payload: { phoneNumber: '6287713848500', ownerId: TEST_OWNER },
      })
      expect(res.json()).toMatchObject({ error_code: 'session_logged_out', retryable: false })
    })

    it('falls back to pairing_failed for an unclassifiable pairing error', async () => {
      const { Boom } = await import('@hapi/boom')
      app = makeApp({
        pairingCode: async () => {
          throw new Boom('something odd', { statusCode: 599 })
        },
      })
      const id = await createSessionVia(app, 'route test pair odd')
      const res = await app.inject({
        method: 'POST',
        url: `/sessions/${id}/pair`,
        headers: auth,
        payload: { phoneNumber: '6287713848500', ownerId: TEST_OWNER },
      })
      expect(res.statusCode).toBe(502)
      expect(res.json().error_code).toBe('pairing_failed')
    })

    it('reports an oversized body as payload_too_large, not invalid_payload', async () => {
      app = makeApp()
      const id = await createSessionVia(app, 'route test big')
      const res = await app.inject({
        method: 'POST',
        url: `/sessions/${id}/send`,
        headers: { ...sendAuth, 'content-type': 'application/json' },
        payload: { idempotencyKey: 'k', to: '6287713848500', type: 'text', text: 'x'.repeat(2_000_000) },
      })
      expect(res.statusCode).toBe(413)
      expect(res.json()).toMatchObject({ error_code: 'payload_too_large', retryable: false })
    })

    it('reports an unsupported content type as unsupported_media_type', async () => {
      app = makeApp()
      const id = await createSessionVia(app, 'route test ctype')
      const res = await app.inject({
        method: 'POST',
        url: `/sessions/${id}/send`,
        headers: { ...sendAuth, 'content-type': 'application/xml' },
        payload: '<send/>',
      })
      expect(res.statusCode).toBe(415)
      expect(res.json()).toMatchObject({ error_code: 'unsupported_media_type', retryable: false })
    })
  })

  describe('GET /sessions/:id/qr status', () => {
    it('falls back to the stored row, not a hardcoded new', async () => {
      app = makeApp()
      const id = await createSessionVia(app, 'route test qr status')
      await pool.query("UPDATE wa_sessions SET status = 'logged_out' WHERE id = $1", [id])

      const res = await app.inject({
        method: 'GET',
        url: `/sessions/${id}/qr?ownerId=${TEST_OWNER}`,
        headers: auth,
      })
      expect(res.json()).toMatchObject({ id, status: 'logged_out', qr: null })
    })
  })

  describe('POST /sessions/:id/reset', () => {
    it('returns a logged-out session to new so it can be paired again', async () => {
      app = makeApp()
      const id = await createSessionVia(app, 'route test reset')
      const res = await app.inject({
        method: 'POST',
        url: `/sessions/${id}/reset`,
        headers: auth,
        payload: { ownerId: TEST_OWNER },
      })
      expect(res.statusCode).toBe(200)
      expect(res.json()).toMatchObject({ id, status: 'new' })
    })

    it('404s an unknown session', async () => {
      app = makeApp()
      const res = await app.inject({
        method: 'POST',
        url: '/sessions/00000000-0000-0000-0000-000000000000/reset',
        headers: auth,
        payload: { ownerId: TEST_OWNER },
      })
      expect(res.statusCode).toBe(404)
    })

    it('404s a session owned by someone else', async () => {
      app = makeApp()
      const id = await createSessionVia(app, 'route test reset wrong owner')
      const res = await app.inject({
        method: 'POST',
        url: `/sessions/${id}/reset`,
        headers: auth,
        payload: { ownerId: 'someone-else' },
      })
      expect(res.statusCode).toBe(404)
    })
  })

  describe('POST /sessions/:id/logout', () => {
    it('reports logged_out', async () => {
      app = makeApp()
      const id = await createSessionVia(app)
      const res = await app.inject({
        method: 'POST',
        url: `/sessions/${id}/logout`,
        headers: auth,
        payload: { ownerId: TEST_OWNER },
      })
      expect(res.statusCode).toBe(200)
      expect(res.json()).toMatchObject({ id, status: 'logged_out' })
    })
  })

  describe('DELETE /sessions/:id', () => {
    it('calls SessionManager.delete and responds 204', async () => {
      let deletedId: string | undefined
      app = makeApp({ delete: async (id: string) => void (deletedId = id) })
      const id = await createSessionVia(app, 'route test delete')

      const res = await app.inject({
        method: 'DELETE',
        url: `/sessions/${id}`,
        headers: auth,
        payload: { ownerId: TEST_OWNER },
      })
      expect(res.statusCode).toBe(204)
      expect(deletedId).toBe(id)
    })

    it('404s an unknown session', async () => {
      app = makeApp()
      const res = await app.inject({
        method: 'DELETE',
        url: '/sessions/00000000-0000-0000-0000-000000000000',
        headers: auth,
        payload: { ownerId: TEST_OWNER },
      })
      expect(res.statusCode).toBe(404)
    })

    it('requires manage, not just read', async () => {
      app = makeApp()
      const id = await createSessionVia(app, 'route test delete auth')
      const res = await app.inject({
        method: 'DELETE',
        url: `/sessions/${id}`,
        headers: sendAuth,
        payload: { ownerId: TEST_OWNER },
      })
      expect(res.statusCode).toBe(403)
    })
  })
})
