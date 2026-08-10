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

describe('subscribeToMessageEvents read receipts', () => {
  function fakeSocket(): { socket: WASocket; readMessages: ReturnType<typeof vi.fn>; trigger: (payload: unknown) => void } {
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
      trigger: (payload) => handlers['messages.upsert']?.(payload as never),
    }
  }

  it('marks an inbound message read after a human-plausible pause', async () => {
    vi.useFakeTimers()
    try {
      const { socket, readMessages, trigger } = fakeSocket()
      subscribeToMessageEvents(socket, 's1', new EventBus(logger), logger)

      trigger({ messages: [message()], type: 'notify' })
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
      trigger({ messages: [own], type: 'notify' })
      await vi.advanceTimersByTimeAsync(5_000)

      expect(readMessages).not.toHaveBeenCalled()
    } finally {
      vi.useRealTimers()
    }
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
