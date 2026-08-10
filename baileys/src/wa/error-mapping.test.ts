import { Boom } from '@hapi/boom'
import { describe, expect, it } from 'vitest'
import { notOnWhatsApp } from '../errors.js'
import { mapSendError, statusCodeOf } from './error-mapping.js'

const boom = (message: string, statusCode: number) => new Boom(message, { statusCode })

describe('mapSendError', () => {
  it('maps a logged-out socket to a permanent failure', () => {
    const err = mapSendError(boom('Connection Failure', 401), 's1')
    expect(err.errorCode).toBe('session_logged_out')
    expect(err.statusCode).toBe(409)
    expect(err.retryable).toBe(false)
  })

  it('maps forbidden to the same permanent failure', () => {
    expect(mapSendError(boom('Forbidden', 403), 's1').errorCode).toBe('session_logged_out')
  })

  it.each([
    ['connectionClosed', 428],
    ['timedOut', 408],
    ['restartRequired', 515],
    ['unavailableService', 503],
  ])('maps %s (%i) to a retryable disconnect', (_name, status) => {
    const err = mapSendError(boom('boom', status), 's1')
    expect(err.errorCode).toBe('session_not_connected')
    expect(err.retryable).toBe(true)
  })

  it('maps 429 to rate limiting', () => {
    const err = mapSendError(boom('slow down', 429), 's1')
    expect(err.errorCode).toBe('rate_limited_by_wa')
    expect(err.statusCode).toBe(429)
    expect(err.retryable).toBe(true)
  })

  it('detects rate limiting reported as text with no status code', () => {
    for (const message of ['rate-overlimit', 'Rate Limit exceeded', 'too many requests']) {
      expect(mapSendError(new Error(message), 's1').errorCode, message).toBe('rate_limited_by_wa')
    }
  })

  it('detects a closed socket reported as text', () => {
    expect(mapSendError(new Error('Connection Closed'), 's1').errorCode).toBe(
      'session_not_connected',
    )
  })

  it('falls back to a retryable send_failed for anything unrecognised', () => {
    const err = mapSendError(new Error('something odd'), 's1')
    expect(err.errorCode).toBe('send_failed')
    expect(err.statusCode).toBe(502)
    expect(err.retryable).toBe(true)
    expect(err.detail).toContain('something odd')
  })

  it('never returns a bare 500', () => {
    for (const input of [new Error('x'), boom('y', 500), null, undefined, 'string error']) {
      expect(mapSendError(input, 's1').statusCode).not.toBe(500)
    }
  })

  it('passes an ApiError through untouched', () => {
    const original = notOnWhatsApp('62812@s.whatsapp.net')
    expect(mapSendError(original, 's1')).toBe(original)
  })
})

describe('statusCodeOf', () => {
  it('reads a Boom status code', () => {
    expect(statusCodeOf(boom('x', 428))).toBe(428)
  })

  it('returns undefined for a plain error or non-error', () => {
    expect(statusCodeOf(new Error('x'))).toBeUndefined()
    expect(statusCodeOf(null)).toBeUndefined()
    expect(statusCodeOf({ output: { statusCode: 'nope' } })).toBeUndefined()
  })
})
