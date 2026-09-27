import { createHmac, timingSafeEqual } from 'node:crypto'

export const SIGNATURE_HEADER = 'x-baileys-signature'
export const TIMESTAMP_HEADER = 'x-baileys-timestamp'
export const EVENT_HEADER = 'x-baileys-event'

export function signPayload(secret: string, timestamp: number, body: string): string {
  return createHmac('sha256', secret).update(`${timestamp}.${body}`).digest('hex')
}

export function verifySignature(
  secret: string,
  timestamp: number,
  body: string,
  signature: string,
): boolean {
  const expected = Buffer.from(signPayload(secret, timestamp, body), 'utf8')
  const actual = Buffer.from(signature, 'utf8')
  if (expected.length !== actual.length) return false
  return timingSafeEqual(expected, actual)
}
