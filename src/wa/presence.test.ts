import { describe, expect, it, vi } from 'vitest'
import { createLogger } from '../logger.js'
import { simulateTyping, typingDelayFor } from './presence.js'

const logger = createLogger('silent')

describe('typingDelayFor', () => {
  it('never goes below the minimum, even for empty text', () => {
    expect(typingDelayFor('')).toBe(600)
  })

  it('never exceeds the maximum, even for long text', () => {
    expect(typingDelayFor('x'.repeat(1000))).toBe(2_500)
  })

  it('scales with text length in between', () => {
    expect(typingDelayFor('hello there')).toBe(600) // 11 chars * 35ms = 385ms, clamped up
    expect(typingDelayFor('x'.repeat(50))).toBe(1_750) // 50 * 35ms
  })
})

describe('simulateTyping', () => {
  it('sends composing then paused around the typing delay', async () => {
    const calls: string[] = []
    const socket = {
      sendPresenceUpdate: vi.fn(async (type: string) => {
        calls.push(type)
      }),
    }

    await simulateTyping(socket, 'x@s.whatsapp.net', 'hi', 5_000, logger)

    expect(calls).toEqual(['composing', 'paused'])
    expect(socket.sendPresenceUpdate).toHaveBeenNthCalledWith(1, 'composing', 'x@s.whatsapp.net')
    expect(socket.sendPresenceUpdate).toHaveBeenNthCalledWith(2, 'paused', 'x@s.whatsapp.net')
  })

  it('swallows a socket that cannot take presence updates', async () => {
    const socket = {
      sendPresenceUpdate: vi.fn(async () => {
        throw new Error('not supported')
      }),
    }

    await expect(simulateTyping(socket, 'x@s.whatsapp.net', 'hi', 5_000, logger)).resolves.toBeUndefined()
  })

  it('times out instead of hanging forever on a stuck socket', async () => {
    const socket = { sendPresenceUpdate: () => new Promise<void>(() => {}) }

    const start = Date.now()
    await expect(simulateTyping(socket, 'x@s.whatsapp.net', 'hi', 25, logger)).resolves.toBeUndefined()
    expect(Date.now() - start).toBeLessThan(1_000)
  })
})
