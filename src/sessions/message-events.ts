import { proto, type WAMessage, type WASocket } from 'baileys'
import type { Logger } from '../logger.js'
import { mapAckError } from '../wa/error-mapping.js'
import { EventBus, nowIso, type SessionEvent } from './events.js'

const STATUS_NAMES: Record<number, string> = Object.fromEntries(
  Object.entries(proto.WebMessageInfo.Status)
    .filter(([, value]) => typeof value === 'number')
    .map(([name, value]) => [value as number, name.toLowerCase()]),
)

export function statusName(status: number | null | undefined): string {
  if (status === null || status === undefined) return 'unknown'
  return STATUS_NAMES[status] ?? `status_${status}`
}

export function extractText(message: WAMessage): string | undefined {
  const content = message.message
  if (!content) return undefined
  return (
    content.conversation ??
    content.extendedTextMessage?.text ??
    content.imageMessage?.caption ??
    content.videoMessage?.caption ??
    content.documentMessage?.caption ??
    undefined
  )
}

export function messageType(message: WAMessage): string {
  const content = message.message
  if (!content) return 'unknown'
  const key = Object.keys(content).find(
    (k) => content[k as keyof typeof content] !== undefined && content[k as keyof typeof content] !== null,
  )
  return key ?? 'unknown'
}

export function toReceivedEvent(sessionId: string, message: WAMessage): SessionEvent | undefined {
  if (message.key.fromMe) return undefined
  if (!message.key.id || !message.key.remoteJid) return undefined

  return {
    type: 'message.received',
    sessionId,
    at: nowIso(),
    messageId: message.key.id,
    from: message.key.remoteJid,
    pushName: message.pushName ?? undefined,
    timestamp: message.messageTimestamp ? Number(message.messageTimestamp) : undefined,
    messageType: messageType(message),
    text: extractText(message),
  }
}

const MIN_READ_DELAY_MS = 1_000
const MAX_READ_DELAY_MS = 4_000

/**
 * Marks an inbound message read after a human-plausible pause. Best-effort and fire-and-
 * forget: a read receipt is a courtesy signal, never something worth blocking or failing
 * message processing over.
 */
function markAsRead(socket: WASocket, key: WAMessage['key'], logger: Logger): void {
  const delayMs = MIN_READ_DELAY_MS + Math.random() * (MAX_READ_DELAY_MS - MIN_READ_DELAY_MS)
  setTimeout(() => {
    socket
      .readMessages([key])
      .catch((err) => logger.debug({ err, messageId: key.id }, 'failed to mark message as read'))
  }, delayMs).unref()
}

export function subscribeToMessageEvents(
  socket: WASocket,
  sessionId: string,
  events: EventBus,
  logger: Logger,
): void {
  socket.ev.on('messages.upsert', ({ messages, type }) => {
    if (type !== 'notify') return
    for (const message of messages) {
      const event = toReceivedEvent(sessionId, message)
      if (!event) continue
      events.emit(event)
      markAsRead(socket, message.key, logger)
    }
  })

  socket.ev.on('messages.update', (updates) => {
    for (const { key, update } of updates) {
      if (!key.id || update.status === undefined || update.status === null) continue
      if (!key.fromMe) continue

      const isError = update.status === proto.WebMessageInfo.Status.ERROR
      // Baileys puts WhatsApp's own ack error (e.g. "463") in the first stub parameter. It has to
      // be carried through: a consumer routes a pooled sender away from a session on a rejection,
      // and flattening every reason into one code makes a bad recipient look like a bad session.
      const waErrorCode = isError ? (update.messageStubParameters?.[0] ?? undefined) : undefined
      if (isError) {
        logger.warn({ sessionId, messageId: key.id, waErrorCode }, 'message rejected by whatsapp')
      }

      events.emit({
        type: 'message.status',
        sessionId,
        at: nowIso(),
        messageId: key.id,
        to: key.remoteJid ?? '',
        status: statusName(update.status),
        ...(isError ? { errorCode: mapAckError(waErrorCode) } : {}),
      })
    }
  })

  logger.debug({ sessionId }, 'subscribed to message events')
}
