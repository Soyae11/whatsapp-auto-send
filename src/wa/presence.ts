import { delay, type WASocket } from 'baileys'
import type { Logger } from '../logger.js'
import { withTimeout } from './timeout.js'

const MIN_TYPING_MS = 600
const MAX_TYPING_MS = 2_500
const TYPING_MS_PER_CHAR = 35

/** How long a person would plausibly take to type this, capped to a believable range. */
export function typingDelayFor(text: string): number {
  return Math.min(MAX_TYPING_MS, Math.max(MIN_TYPING_MS, text.length * TYPING_MS_PER_CHAR))
}

/**
 * Shows "typing..." before a send so the account behaves like someone composing a message
 * instead of a script firing text the instant it's queued. Best-effort: a socket that can't
 * take presence updates (or is slow to) must never block or fail the actual send.
 */
export async function simulateTyping(
  socket: Pick<WASocket, 'sendPresenceUpdate'>,
  jid: string,
  text: string,
  timeoutMs: number,
  logger: Logger,
): Promise<void> {
  try {
    await withTimeout(
      'presence',
      timeoutMs,
      (async () => {
        await socket.sendPresenceUpdate('composing', jid)
        await delay(typingDelayFor(text))
        await socket.sendPresenceUpdate('paused', jid)
      })(),
    )
  } catch (err) {
    logger.debug({ err, jid }, 'presence update failed, sending without a typing indicator')
  }
}
