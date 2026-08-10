import { afterEach, describe, expect, it } from 'vitest'
import type { AppInstance } from './app.js'
import type { Config } from './config.js'
import type { Pool } from './db.js'
import { createLogger } from './logger.js'
import { buildServer } from './server.js'
import { SessionManager } from './sessions/manager.js'

const API_KEY = 'k'.repeat(32)
const CONSOLE_KEY = 'c'.repeat(32)

const config: Config = {
  DATABASE_URL: 'postgres://unused',
  PORT: 0,
  LOG_LEVEL: 'silent',
  DISPATCHER_API_KEY: API_KEY,
  CONSOLE_API_KEY: CONSOLE_KEY,
  WEBHOOK_TIMEOUT_MS: 5_000,
}

function stubPool(up: boolean): Pool {
  return {
    query: async () => {
      if (!up) throw new Error('connection refused')
      return { rows: [] }
    },
  } as unknown as Pool
}

function makeApp(dbUp = true): AppInstance {
  const pool = stubPool(dbUp)
  return buildServer({
    config,
    pool,
    logger: createLogger('silent'),
    sessions: new SessionManager(pool, createLogger('silent')),
  })
}

let app: AppInstance | undefined

afterEach(async () => {
  await app?.close()
  app = undefined
})

describe('GET /health', () => {
  it('returns 200 with db ok when Postgres answers', async () => {
    app = makeApp(true)
    const res = await app.inject({ method: 'GET', url: '/health' })
    expect(res.statusCode).toBe(200)
    expect(res.json()).toMatchObject({ status: 'ok', db: 'ok' })
    expect(typeof res.json().uptime).toBe('number')
  })

  it('reports db down without throwing when Postgres is unreachable', async () => {
    app = makeApp(false)
    const res = await app.inject({ method: 'GET', url: '/health' })
    expect(res.statusCode).toBe(503)
    expect(res.json()).toMatchObject({ status: 'degraded', db: 'down' })
  })

  it('needs no bearer token', async () => {
    app = makeApp()
    const res = await app.inject({ method: 'GET', url: '/health', headers: {} })
    expect(res.statusCode).toBe(200)
  })
})

describe('bearer auth', () => {
  it('rejects a request with no Authorization header', async () => {
    app = makeApp()
    const res = await app.inject({ method: 'GET', url: '/no-such-route' })
    expect(res.statusCode).toBe(401)
    expect(res.json()).toMatchObject({ error_code: 'unauthorized', retryable: false })
  })

  it('rejects a wrong token', async () => {
    app = makeApp()
    const res = await app.inject({
      method: 'GET',
      url: '/no-such-route',
      headers: { authorization: `Bearer ${'x'.repeat(32)}` },
    })
    expect(res.statusCode).toBe(401)
  })

  it('rejects a non-bearer scheme carrying the right secret', async () => {
    app = makeApp()
    const res = await app.inject({
      method: 'GET',
      url: '/no-such-route',
      headers: { authorization: `Basic ${API_KEY}` },
    })
    expect(res.statusCode).toBe(401)
  })

  it('lets an authenticated request through to the 404 handler', async () => {
    app = makeApp()
    const res = await app.inject({
      method: 'GET',
      url: '/no-such-route',
      headers: { authorization: `Bearer ${API_KEY}` },
    })
    expect(res.statusCode).toBe(404)
    expect(res.json()).toMatchObject({ error_code: 'not_found' })
  })

  it('protects paths that carry a query string', async () => {
    app = makeApp()
    const res = await app.inject({ method: 'GET', url: '/no-such-route?limit=1' })
    expect(res.statusCode).toBe(401)
  })
})

describe('capability scoping', () => {
  // The whole point of splitting the token: the console can run the session lifecycle but
  // cannot put a message on the wire, and the dispatcher can send but cannot log a number
  // out from under itself. Both refusals happen in the onRequest hook, before any handler.

  it('refuses to let the console credential send', async () => {
    app = makeApp()
    const res = await app.inject({
      method: 'POST',
      url: '/sessions/main/send',
      headers: { authorization: `Bearer ${CONSOLE_KEY}` },
      payload: { to: '628123456789', text: 'hi', idempotency_key: 'k' },
    })
    expect(res.statusCode).toBe(403)
    expect(res.json()).toMatchObject({ error_code: 'forbidden', retryable: false })
  })

  it('refuses to let the dispatcher credential manage a session', async () => {
    app = makeApp()
    for (const url of [
      '/sessions/main/logout',
      '/sessions/main/reset',
      '/sessions/main/restart',
      '/sessions/main/connect',
      '/sessions/main/pair',
      '/sessions',
    ]) {
      const res = await app.inject({
        method: 'POST',
        url,
        headers: { authorization: `Bearer ${API_KEY}` },
        payload: {},
      })
      expect(res.statusCode, url).toBe(403)
    }
  })

  it('refuses to let the dispatcher credential read a QR', async () => {
    app = makeApp()
    for (const url of ['/sessions/main/qr', '/sessions/main/qr/stream']) {
      const res = await app.inject({
        method: 'GET',
        url,
        headers: { authorization: `Bearer ${API_KEY}` },
      })
      expect(res.statusCode, url).toBe(403)
    }
  })

  it('lets both credentials read session status', async () => {
    app = makeApp()
    for (const key of [API_KEY, CONSOLE_KEY]) {
      for (const url of ['/sessions', '/sessions/main']) {
        const res = await app.inject({
          method: 'GET',
          url,
          headers: { authorization: `Bearer ${key}` },
        })
        // Whatever the stub pool makes of it, the request must not be turned away.
        expect([401, 403], url).not.toContain(res.statusCode)
      }
    }
  })

  it('turns console access off when no console key is configured', async () => {
    const pool = stubPool(true)
    app = buildServer({
      config: { ...config, CONSOLE_API_KEY: undefined },
      pool,
      logger: createLogger('silent'),
      sessions: new SessionManager(pool, createLogger('silent')),
    })
    const res = await app.inject({
      method: 'GET',
      url: '/sessions',
      headers: { authorization: `Bearer ${CONSOLE_KEY}` },
    })
    expect(res.statusCode).toBe(401)
  })
})
