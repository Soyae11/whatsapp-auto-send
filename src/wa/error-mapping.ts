import { DisconnectReason } from 'baileys'
import {
  ApiError,
  pairingFailed,
  rateLimitedByWa,
  sendFailed,
  sessionLoggedOut,
  sessionNotConnected,
} from '../errors.js'

interface BoomLike {
  output?: { statusCode?: number }
  message?: string
}

export function statusCodeOf(err: unknown): number | undefined {
  const status = (err as BoomLike | undefined)?.output?.statusCode
  return typeof status === 'number' ? status : undefined
}

export function isWhatsAppError(err: unknown): boolean {
  return statusCodeOf(err) !== undefined
}

/**
 * Codes WhatsApp puts on a rejected message's ack. Only the two we can act on differently are
 * named; everything else takes the 'send_failed' default, which consumers read as the session's
 * fault. 463 is a restriction on the sending account, so another number can carry the message.
 * 404 is about the recipient JID and would fail identically on any session.
 *
 * A Map rather than an object literal: waErrorCode comes straight off the wire, and an object
 * lookup resolves inherited keys — 'constructor', 'toString' — to a function, which `??` has no
 * reason to replace. The event would then carry a function as its errorCode, JSON.stringify would
 * drop the key, and the consumer would see no reason at all.
 */
const ACK_ERROR_CODES = new Map<string, string>([
  ['404', 'not_on_whatsapp'],
  ['463', 'message_rejected'],
])

export function mapAckError(waErrorCode: string | undefined): string {
  if (waErrorCode === undefined) return 'send_failed'
  return ACK_ERROR_CODES.get(waErrorCode) ?? 'send_failed'
}

export function mapSendError(err: unknown, sessionId: string): ApiError {
  return mapWhatsAppError(err, sessionId, sendFailed)
}

export function mapPairingError(err: unknown, sessionId: string): ApiError {
  return mapWhatsAppError(err, sessionId, pairingFailed)
}

export function mapWhatsAppError(
  err: unknown,
  sessionId: string,
  fallback: (message: string, detail?: string) => ApiError = sendFailed,
): ApiError {
  if (err instanceof ApiError) return err

  const status = statusCodeOf(err)
  const message = (err as Error | undefined)?.message ?? 'send failed'
  const detail = `${status ?? 'no-status'}: ${message}`

  switch (status) {
    case DisconnectReason.loggedOut: // 401
    case DisconnectReason.forbidden: // 403
      return sessionLoggedOut(sessionId)

    case DisconnectReason.connectionClosed: // 428
    case DisconnectReason.connectionLost: // 408, also timedOut
    case DisconnectReason.restartRequired: // 515
    case DisconnectReason.unavailableService: // 503
      return sessionNotConnected(sessionId, 'disconnected')

    case 429:
      return rateLimitedByWa(message)

    default:
      break
  }

  if (/rate.?over.?limit|too many|rate.?limit/i.test(message)) {
    return rateLimitedByWa(message)
  }
  if (/connection closed|not open|websocket/i.test(message)) {
    return sessionNotConnected(sessionId, 'disconnected')
  }

  return fallback(message, detail)
}
