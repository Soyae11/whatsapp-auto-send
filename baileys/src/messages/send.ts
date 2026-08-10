import type { Pool } from '../db.js'
import { ApiError, notOnWhatsApp, sendInProgress } from '../errors.js'
import type { Logger } from '../logger.js'
import type { MetricsRegistry } from '../metrics/registry.js'
import type { SessionManager } from '../sessions/manager.js'
import { mapSendError } from '../wa/error-mapping.js'
import { withJakartaFooter } from '../wa/footer.js'
import { simulateTyping } from '../wa/presence.js'
import { WA_TIMEOUTS, withTimeout } from '../wa/timeout.js'
import { InvalidPhoneNumberError, toUserJid } from '../wa/phone.js'
import { claimSend, markFailed, markSent } from './repository.js'

export interface SendRequest {
  idempotencyKey: string
  to: string
  type: 'text'
  text: string
}

export interface SendResult {
  idempotencyKey: string
  to: string
  status: 'sent'
  waMessageId: string | null
  deduplicated: boolean
}

export async function sendTextMessage(
  deps: {
    pool: Pool
    sessions: SessionManager
    logger: Logger
    metrics?: MetricsRegistry
    /** Overridable so tests can prove the wiring without waiting out the real bounds. */
    timeouts?: { onWhatsApp: number; sendMessage: number; presence?: number }
  },
  sessionId: string,
  request: SendRequest,
): Promise<SendResult> {
  const { pool, sessions, logger, metrics, timeouts = WA_TIMEOUTS } = deps
  const log = logger.child({ sessionId, idempotencyKey: request.idempotencyKey })

  let toJid: string
  try {
    toJid = toUserJid(request.to)
  } catch (err) {
    if (err instanceof InvalidPhoneNumberError) throw new ApiError(400, 'invalid_payload', err.message, false)
    throw err
  }

  const claim = await claimSend(pool, {
    sessionId,
    idempotencyKey: request.idempotencyKey,
    toJid,
    payload: { type: request.type, text: request.text },
  })

  if (claim.outcome === 'duplicate') {
    metrics?.increment('baileys_sends_total', { session_id: sessionId, outcome: 'deduplicated' })
    log.info({ waMessageId: claim.row.wa_message_id }, 'deduplicated send')
    return {
      idempotencyKey: request.idempotencyKey,
      to: claim.row.to_jid,
      status: 'sent',
      waMessageId: claim.row.wa_message_id,
      deduplicated: true,
    }
  }

  if (claim.outcome === 'in_flight') throw sendInProgress(request.idempotencyKey)

  try {
    const socket = sessions.requireConnectedSocket(sessionId)

    const [check] =
      (await withTimeout('onWhatsApp', timeouts.onWhatsApp, socket.onWhatsApp(toJid))) ?? []
    if (!check?.exists) {
      throw notOnWhatsApp(request.to)
    }

    await simulateTyping(socket, check.jid, request.text, timeouts.presence ?? WA_TIMEOUTS.presence, log)

    const sent = await withTimeout(
      'sendMessage',
      timeouts.sendMessage,
      socket.sendMessage(check.jid, { text: withJakartaFooter(request.text) }),
    )
    const waMessageId = sent?.key?.id ?? null

    await markSent(pool, request.idempotencyKey, waMessageId, check.jid)
    sessions.recordSendOutcome(sessionId, { ok: true })
    metrics?.increment('baileys_sends_total', { session_id: sessionId, outcome: 'sent' })
    log.info({ to: check.jid, waMessageId }, 'message sent')

    return {
      idempotencyKey: request.idempotencyKey,
      to: check.jid,
      status: 'sent',
      waMessageId,
      deduplicated: false,
    }
  } catch (err) {
    const apiError = mapSendError(err, sessionId)

    await markFailed(
      pool,
      request.idempotencyKey,
      apiError.errorCode,
      apiError.detail ?? apiError.message,
    ).catch((dbErr) => log.error({ err: dbErr }, 'failed to record send failure'))

    sessions.recordSendOutcome(sessionId, { ok: false, errorCode: apiError.errorCode })
    metrics?.increment('baileys_sends_total', { session_id: sessionId, outcome: 'failed' })
    metrics?.increment('baileys_send_failures_total', {
      session_id: sessionId,
      error_code: apiError.errorCode,
    })
    log.warn(
      {
        errorCode: apiError.errorCode,
        retryable: apiError.retryable,
        to: toJid,
        detail: apiError.detail,
      },
      'send failed',
    )
    throw apiError
  }
}
