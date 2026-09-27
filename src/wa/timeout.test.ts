import { describe, expect, it } from 'vitest'
import { ApiError } from '../errors.js'
import { withTimeout, WA_TIMEOUTS } from './timeout.js'

describe('withTimeout', () => {
  it('passes a result through untouched', async () => {
    await expect(withTimeout('op', 1000, Promise.resolve('done'))).resolves.toBe('done')
  })

  it('rejects with a usable error code when the call hangs', async () => {
    const never = new Promise<never>(() => {})
    const error = await withTimeout('sendMessage', 20, never).catch((e: unknown) => e)

    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({
      errorCode: 'upstream_timeout',
      statusCode: 504,
      retryable: true,
    })
    expect((error as ApiError).message).toContain('sendMessage')
    expect((error as ApiError).message).toContain('20ms')
  })

  it('propagates the original rejection rather than masking it', async () => {
    const boom = new Error('connection closed')
    await expect(withTimeout('op', 1000, Promise.reject(boom))).rejects.toBe(boom)
  })

  it('does not fire after the work settles', async () => {
    await withTimeout('op', 30, Promise.resolve('fast'))
    await new Promise((r) => setTimeout(r, 60))
  })

  it('bounds every WhatsApp call well under Baileys 60s query default', () => {
    for (const [name, ms] of Object.entries(WA_TIMEOUTS)) {
      expect(ms, name).toBeGreaterThan(0)
      expect(ms, name).toBeLessThanOrEqual(30_000)
    }
  })
})
