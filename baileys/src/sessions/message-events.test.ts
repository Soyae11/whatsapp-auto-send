import { proto, type WAMessage, type WASocket } from 'baileys'
import { describe, expect, it, vi } from 'vitest'
import { createLogger } from '../logger.js'
import { EventBus } from './events.js'
import { extractText, messageType, statusName, subscribeToMessageEvents, toReceivedEvent } from './message-events.js'

const logger = createLogger('silent')

const message = (overrides: Partial<WAMessage> = {}): WAMessage =>
  ({
    key: { id: 'MSG-1', remoteJid: '6287713848500@s.whatsapp.net', fromMe: false },
    pushName: 'Tester',
    messageTimestamp: 1786180000,
    message: { conversation: 'hello' },
    ...overrides,
  }) as WAMessage

describe('toReceivedEvent', () => {
  it('translates an inbound text message', () => {
    expect(toReceivedEvent('s1', message())).toMatchObject({
      type: 'message.received',
      sessionId: 's1',
      messageId: 'MSG-1',
      from: '6287713848500@s.whatsapp.net',
      pushName: 'Tester',
      messageType: 'conversation',
      text: 'hello',
    })
  })

  it('ignores messages this service sent', () => {
    const own = message({ key: { id: 'MSG-2', remoteJid: 'x@s.whatsapp.net', fromMe: true } })
    expect(toReceivedEvent('s1', own)).toBeUndefined()
  })

  it('ignores a message with no id or no sender', () => {
    expect(toReceivedEvent('s1', message({ key: { id: null, remoteJid: 'x', fromMe: false } }))).toBeUndefined()
    expect(toReceivedEvent('s1', message({ key: { id: 'a', remoteJid: null, fromMe: false } }))).toBeUndefined()
  })

  it('carries a null text through for non-text messages', () => {
    const sticker = message({ message: { stickerMessage: { url: 'x' } } as proto.IMessage })
    expect(toReceivedEvent('s1', sticker)).toMatchObject({ messageType: 'stickerMessage', text: undefined })
  })
})

describe('extractText', () => {
  it('reads the shapes WhatsApp actually uses', () => {
    expect(extractText(message())).toBe('hello')
    expect(extractText(message({ message: { extendedTextMessage: { text: 'linked' } } }))).toBe('linked')
    expect(
      extractText(message({ message: { imageMessage: { caption: 'a photo' } } as proto.IMessage })),
    ).toBe('a photo')
  })

  it('returns undefined when there is no text and no message body', () => {
    expect(extractText(message({ message: null }))).toBeUndefined()
    expect(extractText(message({ message: { stickerMessage: {} } as proto.IMessage }))).toBeUndefined()
  })
})

describe('messageType', () => {
  it('names the populated field', () => {
    expect(messageType(message())).toBe('conversation')
    expect(messageType(message({ message: null }))).toBe('unknown')
  })
})

describe('statusName', () => {
  it('maps proto status numbers to readable names', () => {
    expect(statusName(proto.WebMessageInfo.Status.DELIVERY_ACK)).toBe('delivery_ack')
    expect(statusName(proto.WebMessageInfo.Status.READ)).toBe('read')
    expect(statusName(proto.WebMessageInfo.Status.SERVER_ACK)).toBe('server_ack')
  })

  it('degrades gracefully for unknown or missing values', () => {
    expect(statusName(null)).toBe('unknown')
    expect(statusName(undefined)).toBe('unknown')
    expect(statusName(9999)).toBe('status_9999')
  })
})

function fakeSocket(): {
  socket: WASocket
  readMessages: ReturnType<typeof vi.fn>
  trigger: (event: string, payload: unknown) => void
} {
  const handlers: Record<string, (payload: never) => void> = {}
  const readMessages = vi.fn(async () => {})
  const socket = {
    ev: {
      on: (event: string, handler: (payload: never) => void) => {
        handlers[event] = handler
      },
    },
    readMessages,
  } as unknown as WASocket
  return {
    socket,
    readMessages,
    trigger: (event, payload) => handlers[event]?.(payload as never),
  }
}

describe('subscribeToMessageEvents read receipts', () => {

  it('marks an inbound message read after a human-plausible pause', async () => {
    vi.useFakeTimers()
    try {
      const { socket, readMessages, trigger } = fakeSocket()
      subscribeToMessageEvents(socket, 's1', new EventBus(logger), logger)

      trigger('messages.upsert', { messages: [message()], type: 'notify' })
      expect(readMessages).not.toHaveBeenCalled()

      await vi.advanceTimersByTimeAsync(4_000)
      expect(readMessages).toHaveBeenCalledWith([message().key])
    } finally {
      vi.useRealTimers()
    }
  })

  it('never marks the service its own outgoing messages read', async () => {
    vi.useFakeTimers()
    try {
      const { socket, readMessages, trigger } = fakeSocket()
      subscribeToMessageEvents(socket, 's1', new EventBus(logger), logger)

      const own = message({ key: { id: 'MSG-2', remoteJid: 'x@s.whatsapp.net', fromMe: true } })
      trigger('messages.upsert', { messages: [own], type: 'notify' })
      await vi.advanceTimersByTimeAsync(5_000)

      expect(readMessages).not.toHaveBeenCalled()
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('subscribeToMessageEvents status updates', () => {
  const key = { id: 'MSG-1', remoteJid: '6287713848500@s.whatsapp.net', fromMe: true }

  it('reports a delivery ack with no error code', () => {
    const { socket, trigger } = fakeSocket()
    const bus = new EventBus(logger)
    const seen = vi.fn()
    bus.subscribe(seen)
    subscribeToMessageEvents(socket, 's1', bus, logger)

    trigger('messages.update', [{ key, update: { status: proto.WebMessageInfo.Status.DELIVERY_ACK } }])

    expect(seen).toHaveBeenCalledWith(
      expect.objectContaining({ type: 'message.status', status: 'delivery_ack' }),
    )
    expect(seen.mock.calls[0]?.[0]).not.toHaveProperty('errorCode')
  })

  it('flags an ERROR status with no ack reason as the session\'s fault, so a "sent" message that WhatsApp actually rejected does not go unreported', () => {
    const { socket, trigger } = fakeSocket()
    const bus = new EventBus(logger)
    const seen = vi.fn()
    bus.subscribe(seen)
    subscribeToMessageEvents(socket, 's1', bus, logger)

    trigger('messages.update', [{ key, update: { status: proto.WebMessageInfo.Status.ERROR } }])

    expect(seen).toHaveBeenCalledWith(
      expect.objectContaining({ type: 'message.status', status: 'error', errorCode: 'send_failed' }),
    )
  })

  // A consumer routes a pooled sender away from a session on a rejection it reads as the
  // session's fault. Flattening every reason into one code made a bad recipient look like a bad
  // session, and a handful of them would retire every member of a pool.
  it.each([
    ['463', 'message_rejected'],
    ['404', 'not_on_whatsapp'],
    ['999', 'send_failed'],
  ])('carries WhatsApp ack error %s through as %s', (waCode, expected) => {
    const { socket, trigger } = fakeSocket()
    const bus = new EventBus(logger)
    const seen = vi.fn()
    bus.subscribe(seen)
    subscribeToMessageEvents(socket, 's1', bus, logger)

    trigger('messages.update', [
      {
        key,
        update: { status: proto.WebMessageInfo.Status.ERROR, messageStubParameters: [waCode] },
      },
    ])

    expect(seen).toHaveBeenCalledWith(
      expect.objectContaining({ type: 'message.status', status: 'error', errorCode: expected }),
    )
  })

  it('ignores updates with no id or that are not this service\'s own message', () => {
    const { socket, trigger } = fakeSocket()
    const bus = new EventBus(logger)
    const seen = vi.fn()
    bus.subscribe(seen)
    subscribeToMessageEvents(socket, 's1', bus, logger)

    trigger('messages.update', [{ key: { ...key, id: null }, update: { status: proto.WebMessageInfo.Status.SERVER_ACK } }])
    trigger('messages.update', [{ key: { ...key, fromMe: false }, update: { status: proto.WebMessageInfo.Status.SERVER_ACK } }])

    expect(seen).not.toHaveBeenCalled()
  })
})

describe('EventBus', () => {
  it('isolates a throwing listener from the others', () => {
    const bus = new EventBus(logger)
    const good = vi.fn()
    bus.subscribe(() => {
      throw new Error('bad subscriber')
    })
    bus.subscribe(good)

    expect(() => bus.emit({ type: 'qr', sessionId: 's', at: 'now', qr: 'x' })).not.toThrow()
    expect(good).toHaveBeenCalledOnce()
  })

  it('stops delivering after unsubscribe and frees the listener', () => {
    const bus = new EventBus(logger)
    const listener = vi.fn()
    const unsubscribe = bus.subscribe(listener)
    expect(bus.listenerCount).toBe(1)

    unsubscribe()
    expect(bus.listenerCount).toBe(0)
    bus.emit({ type: 'qr', sessionId: 's', at: 'now', qr: 'x' })
    expect(listener).not.toHaveBeenCalled()
  })
})
