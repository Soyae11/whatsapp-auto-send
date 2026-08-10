import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'
import type { AppInstance } from '../app.js'
import type { Config } from '../config.js'
import { createPool, type Pool } from '../db.js'
import { createLogger } from '../logger.js'
import { buildServer } from '../server.js'
import { EventBus } from '../sessions/events.js'
import { SessionManager } from '../sessions/manager.js'
import { createSession } from '../sessions/repository.js'

const url = process.env.DATABASE_URL
const suite = url ? describe : describe.skip

const API_KEY = 'k'.repeat(32)
const logger = createLogger('silent')

suite('GET /sessions/:id/qr/stream', () => {
  let pool: Pool
  let app: AppInstance | undefined
  let events: EventBus
  let baseUrl: string

  const config: Config = {
    DATABASE_URL: url ?? '',
    PORT: 0,
    LOG_LEVEL: 'silent',
    // The QR stream is a `manage` route, so it is the console credential that reaches it.
    DISPATCHER_API_KEY: 'k'.repeat(31) + 'd',
    CONSOLE_API_KEY: API_KEY,
    WEBHOOK_TIMEOUT_MS: 5_000,
  }

  beforeAll(async () => {
    pool = createPool(url!, logger)
  })

  afterEach(async () => {
    await app?.close()
    app = undefined
  })

  afterAll(async () => {
    await pool.query("DELETE FROM wa_sessions WHERE label LIKE 'sse test%'")
    await pool.end()
  })

  async function start(snapshot?: () => unknown): Promise<string> {
    events = new EventBus(logger)
    const sessions = new SessionManager(pool, logger, events)
    if (snapshot) Object.assign(sessions, { snapshot })
    app = buildServer({ config, pool, logger, sessions })
    await app.listen({ port: 0, host: '127.0.0.1' })
    const address = app.server.address()
    if (typeof address === 'string' || address === null) throw new Error('no port')
    baseUrl = `http://127.0.0.1:${address.port}`
    return (await createSession(pool, 'sse test')).id
  }

  async function readFrames(
    res: Response,
    want: number,
    timeoutMs = 3000,
  ): Promise<{ event: string; data: unknown }[]> {
    const reader = res.body!.getReader()
    const decoder = new TextDecoder()
    const frames: { event: string; data: unknown }[] = []
    let buffer = ''
    const deadline = Date.now() + timeoutMs

    while (frames.length < want && Date.now() < deadline) {
      const chunk = await Promise.race([
        reader.read(),
        new Promise<{ done: true; value: undefined }>((r) =>
          setTimeout(() => r({ done: true, value: undefined }), deadline - Date.now()),
        ),
      ])
      if (chunk.done) break
      buffer += decoder.decode(chunk.value, { stream: true })

      let split: number
      while ((split = buffer.indexOf('\n\n')) !== -1) {
        const raw = buffer.slice(0, split)
        buffer = buffer.slice(split + 2)
        const eventLine = raw.split('\n').find((l) => l.startsWith('event: '))
        const dataLine = raw.split('\n').find((l) => l.startsWith('data: '))
        if (eventLine && dataLine) {
          frames.push({ event: eventLine.slice(7), data: JSON.parse(dataLine.slice(6)) })
        }
      }
    }
    void reader.cancel().catch(() => {})
    return frames
  }

  const open = (id: string, signal?: AbortSignal): Promise<Response> =>
    fetch(`${baseUrl}/sessions/${id}/qr/stream`, {
      headers: { authorization: `Bearer ${API_KEY}` },
      ...(signal ? { signal } : {}),
    })

  it('opens as an event stream with buffering disabled', async () => {
    const id = await start()
    const res = await open(id)

    expect(res.status).toBe(200)
    expect(res.headers.get('content-type')).toContain('text/event-stream')
    expect(res.headers.get('cache-control')).toContain('no-cache')
    expect(res.headers.get('x-accel-buffering')).toBe('no')
    await res.body?.cancel()
  })

  it('requires a bearer token', async () => {
    const id = await start()
    const res = await fetch(`${baseUrl}/sessions/${id}/qr/stream`)
    expect(res.status).toBe(401)
  })

  it('404s an unknown session before hijacking the socket', async () => {
    await start()
    const res = await fetch(`${baseUrl}/sessions/00000000-0000-0000-0000-000000000000/qr/stream`, {
      headers: { authorization: `Bearer ${API_KEY}` },
    })
    expect(res.status).toBe(404)
    expect(((await res.json()) as { error_code: string }).error_code).toBe('session_not_found')
  })

  it('sends the current status immediately, then each new QR as it rotates', async () => {
    const id = await start()
    const res = await open(id)
    const framesPromise = readFrames(res, 3)

    await new Promise((r) => setTimeout(r, 100))
    events.emit({ type: 'qr', sessionId: id, at: 'now', qr: 'qr-one' })
    events.emit({ type: 'qr', sessionId: id, at: 'now', qr: 'qr-two' })

    const frames = await framesPromise
    expect(frames[0]).toMatchObject({ event: 'status' })
    expect(frames[1]).toMatchObject({ event: 'qr', data: { qr: 'qr-one' } })
    expect(frames[2]).toMatchObject({ event: 'qr', data: { qr: 'qr-two' } })
  })

  it('replays the QR already in flight so a late subscriber is not left waiting', async () => {
    const id = await start(() => ({
      id: 'x',
      status: 'pairing',
      qr: 'already-showing',
      phoneNumber: undefined,
      lastConnectedAt: undefined,
      reconnectAttempts: 0,
      socketConnected: true,
      lastSuccessfulSendAt: undefined,
      lastFailedSendAt: undefined,
      consecutiveFailures: 0,
      lastErrorCode: undefined,
      nextReconnectInMs: undefined,
    }))
    const res = await open(id)
    const frames = await readFrames(res, 2)
    expect(frames[1]).toMatchObject({ event: 'qr', data: { qr: 'already-showing' } })
  })

  it('emits paired and closes the stream when the session connects', async () => {
    const id = await start()
    const res = await open(id)
    const framesPromise = readFrames(res, 4)

    await new Promise((r) => setTimeout(r, 100))
    events.emit({ type: 'qr', sessionId: id, at: 'now', qr: 'qr-one' })
    events.emit({
      type: 'session.status_changed',
      sessionId: id,
      at: 'now',
      status: 'connected',
      phoneNumber: '6287713848500',
    })

    const frames = await framesPromise
    const paired = frames.find((f) => f.event === 'paired')
    expect(paired).toMatchObject({ data: { phoneNumber: '6287713848500' } })
    expect(events.listenerCount).toBe(0)
  })

  it('reports failure and closes when the session is logged out', async () => {
    const id = await start()
    const res = await open(id)
    const framesPromise = readFrames(res, 3)

    await new Promise((r) => setTimeout(r, 100))
    events.emit({
      type: 'session.status_changed',
      sessionId: id,
      at: 'now',
      status: 'logged_out',
      phoneNumber: undefined,
    })

    const frames = await framesPromise
    expect(frames.some((f) => f.event === 'failed')).toBe(true)
    expect(events.listenerCount).toBe(0)
  })

  it('ignores events belonging to other sessions', async () => {
    const id = await start()
    const res = await open(id)
    const framesPromise = readFrames(res, 2, 800)

    await new Promise((r) => setTimeout(r, 100))
    events.emit({ type: 'qr', sessionId: 'someone-else', at: 'now', qr: 'not-yours' })

    const frames = await framesPromise
    expect(frames.every((f) => JSON.stringify(f.data).includes('not-yours') === false)).toBe(true)
  })

  it('unsubscribes when the client disconnects, so listeners do not leak', async () => {
    const id = await start()
    const controller = new AbortController()
    const res = await open(id, controller.signal)
    await readFrames(res, 1)
    expect(events.listenerCount).toBe(1)

    controller.abort()

    for (let i = 0; i < 40 && events.listenerCount > 0; i += 1) {
      await new Promise((r) => setTimeout(r, 50))
    }
    expect(events.listenerCount).toBe(0)
  })
})
