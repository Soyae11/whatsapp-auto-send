import { describe, expect, it } from 'vitest'
import { backoffDelay, initialLiveStatus, RECONNECT_LIMITS } from './manager.js'

describe('initialLiveStatus', () => {
  it('never starts a socket already claiming to be connected', () => {
    expect(initialLiveStatus('connected')).toBe('disconnected')
  })

  it('carries every other stored status through unchanged', () => {
    for (const status of ['new', 'pairing', 'disconnected', 'logged_out'] as const) {
      expect(initialLiveStatus(status), status).toBe(status)
    }
  })
})

describe('backoffDelay', () => {
  const { RECONNECT_MAX_MS, MAX_RECONNECT_ATTEMPTS } = RECONNECT_LIMITS

  it('grows exponentially', () => {
    const first = backoffDelay(1)
    const fourth = backoffDelay(4)
    expect(first).toBeGreaterThanOrEqual(1600)
    expect(first).toBeLessThanOrEqual(2400)
    expect(fourth).toBeGreaterThan(first * 3)
  })

  it('never exceeds the five-minute cap, however many attempts', () => {
    for (let attempt = 1; attempt <= 50; attempt += 1) {
      expect(backoffDelay(attempt)).toBeLessThanOrEqual(RECONNECT_MAX_MS * 1.2)
    }
  })

  it('is already capped by the time the attempt budget runs out', () => {
    expect(backoffDelay(MAX_RECONNECT_ATTEMPTS)).toBeGreaterThanOrEqual(RECONNECT_MAX_MS * 0.8)
  })

  it('applies jitter so sessions that dropped together do not reconnect in lockstep', () => {
    const samples = new Set(Array.from({ length: 25 }, () => backoffDelay(3)))
    expect(samples.size).toBeGreaterThan(1)
  })
})
