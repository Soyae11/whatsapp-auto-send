import { afterEach, describe, expect, it } from 'vitest'
import type { AppInstance } from './app.js'
import type { Config } from './config.js'
import type { Pool } from './db.js'
import { createLogger } from './logger.js'
import { buildServer } from './server.js'
import { SessionManager } from './sessions/manager.js'

const API_KEY = 'k'.repeat(32)

const config: Config = {
  DATABASE_URL: 'postgres://unused',
  PORT: 0,
  LOG_LEVEL: 'silent',
  API_KEY,
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
