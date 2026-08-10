import { describe, expect, it, vi } from 'vitest'
import { createLogger } from '../logger.js'
import { EventBus, type SessionEvent } from '../sessions/events.js'
import { WebhookDispatcher } from './dispatcher.js'
import { EVENT_HEADER, SIGNATURE_HEADER, TIMESTAMP_HEADER, verifySignature } from './signature.js'

const SECRET = 's'.repeat(32)
const logger = createLogger('silent')

const received: SessionEvent = {
  type: 'message.received',
  sessionId: 'sess-1',
  at: '2026-08-08T00:00:00.000Z',
  messageId: 'MSG-1',
  from: '6287713848500@s.whatsapp.net',
  pushName: 'Tester',
  timestamp: 1786180000,
  messageType: 'conversation',
  text: 'hello',
}

const qrEvent: SessionEvent = {
  type: 'qr',
  sessionId: 'sess-1',
  at: '2026-08-08T00:00:00.000Z',
  qr: '2@secret-pairing-material',
}

function makeDispatcher(fetchImpl: typeof fetch, timeoutMs = 5000) {
  return new WebhookDispatcher({ url: 'https://consumer.test/hook', secret: SECRET, timeoutMs, logger, fetchImpl })
}

const ok = () => new Response('', { status: 200 })

describe('WebhookDispatcher', () => {
  it('posts a signed, verifiable payload', async () => {
    const fetchImpl = vi.fn(async () => ok()) as unknown as typeof fetch
    await makeDispatcher(fetchImpl).deliver(received)

    expect(fetchImpl).toHaveBeenCalledOnce()
    const [url, init] = (fetchImpl as unknown as ReturnType<typeof vi.fn>).mock.calls[0]!
    expect(url).toBe('https://consumer.test/hook')

    const headers = init.headers as Record<string, string>
    expect(headers[EVENT_HEADER]).toBe('message.received')
    expect(JSON.parse(init.body as string)).toEqual(received)

    const timestamp = Number(headers[TIMESTAMP_HEADER])
    const signature = headers[SIGNATURE_HEADER]!.replace('sha256=', '')
    expect(verifySignature(SECRET, timestamp, init.body as string, signature)).toBe(true)
  })

  it('produces a signature that fails if the body is tampered with', async () => {
    const fetchImpl = vi.fn(async () => ok()) as unknown as typeof fetch
    await makeDispatcher(fetchImpl).deliver(received)
    const [, init] = (fetchImpl as unknown as ReturnType<typeof vi.fn>).mock.calls[0]!
    const headers = init.headers as Record<string, string>
    const signature = headers[SIGNATURE_HEADER]!.replace('sha256=', '')

    const tampered = (init.body as string).replace('hello', 'goodbye')
    expect(verifySignature(SECRET, Number(headers[TIMESTAMP_HEADER]), tampered, signature)).toBe(false)
  })

  it('produces a signature that fails if the timestamp is replayed', async () => {
    const fetchImpl = vi.fn(async () => ok()) as unknown as typeof fetch
    await makeDispatcher(fetchImpl).deliver(received)
    const [, init] = (fetchImpl as unknown as ReturnType<typeof vi.fn>).mock.calls[0]!
    const headers = init.headers as Record<string, string>
    const signature = headers[SIGNATURE_HEADER]!.replace('sha256=', '')

    // The timestamp is inside the signed material, so moving it invalidates the signature.
    const later = Number(headers[TIMESTAMP_HEADER]) + 60_000
    expect(verifySignature(SECRET, later, init.body as string, signature)).toBe(false)
  })

  it('never delivers the QR — it is live pairing material', () => {
    const fetchImpl = vi.fn(async () => ok()) as unknown as typeof fetch
    const bus = new EventBus(logger)
    makeDispatcher(fetchImpl).attach(bus)

    bus.emit(qrEvent)
    expect(fetchImpl).not.toHaveBeenCalled()

    bus.emit(received)
    expect(fetchImpl).toHaveBeenCalledOnce()
  })

  it('delivers all three documented event types', () => {
    const fetchImpl = vi.fn(async () => ok()) as unknown as typeof fetch
    const bus = new EventBus(logger)
    makeDispatcher(fetchImpl).attach(bus)

    bus.emit(received)
    bus.emit({ type: 'message.status', sessionId: 's', at: 'now', messageId: 'm', to: 'x', status: 'delivery_ack' })
    bus.emit({ type: 'session.status_changed', sessionId: 's', at: 'now', status: 'connected', phoneNumber: '628' })

    expect(fetchImpl).toHaveBeenCalledTimes(3)
  })

  it('does not block the emitter on a slow consumer', () => {
    let resolveFetch: (r: Response) => void = () => {}
    const fetchImpl = vi.fn(
      () => new Promise<Response>((resolve) => (resolveFetch = resolve)),
    ) as unknown as typeof fetch
    const bus = new EventBus(logger)
    const dispatcher = makeDispatcher(fetchImpl)
    dispatcher.attach(bus)

    const before = Date.now()
    bus.emit(received)
    expect(Date.now() - before).toBeLessThan(50)
    expect(dispatcher.pending).toBe(1)
    resolveFetch(ok())
  })

  it('swallows a rejected delivery without retrying', async () => {
    const fetchImpl = vi.fn(async () => {
      throw new Error('ECONNREFUSED')
    }) as unknown as typeof fetch

    await expect(makeDispatcher(fetchImpl).deliver(received)).resolves.toBeUndefined()
    expect(fetchImpl).toHaveBeenCalledOnce()
  })

  it('swallows a non-2xx response without retrying', async () => {
    const fetchImpl = vi.fn(async () => new Response('nope', { status: 500 })) as unknown as typeof fetch
    await expect(makeDispatcher(fetchImpl).deliver(received)).resolves.toBeUndefined()
    expect(fetchImpl).toHaveBeenCalledOnce()
  })

  it('passes an abort signal so a hung consumer cannot pile up', async () => {
    const fetchImpl = vi.fn(async () => ok()) as unknown as typeof fetch
    await makeDispatcher(fetchImpl, 250).deliver(received)
    const [, init] = (fetchImpl as unknown as ReturnType<typeof vi.fn>).mock.calls[0]!
    expect(init.signal).toBeInstanceOf(AbortSignal)
  })

  it('stops delivering once detached', () => {
    const fetchImpl = vi.fn(async () => ok()) as unknown as typeof fetch
    const bus = new EventBus(logger)
    const detach = makeDispatcher(fetchImpl).attach(bus)

    detach()
    bus.emit(received)
    expect(fetchImpl).not.toHaveBeenCalled()
  })
})
