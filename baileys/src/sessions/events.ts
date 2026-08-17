import type { Logger } from '../logger.js'
import type { SessionStatus } from './repository.js'

export type SessionEvent =
  | { type: 'qr'; sessionId: string; at: string; qr: string }
  | {
      type: 'session.status_changed'
      sessionId: string
      at: string
      status: SessionStatus
      phoneNumber: string | undefined
    }
  | {
      type: 'message.received'
      sessionId: string
      at: string
      messageId: string
      from: string
      pushName: string | undefined
      timestamp: number | undefined
      messageType: string
      text: string | undefined
    }
  | {
      type: 'message.status'
      sessionId: string
      at: string
      messageId: string
      to: string
      status: string
      /**
       * Only set when status is 'error', in the same vocabulary /send errors use. It is the only
       * channel a consumer has for learning that a "sent" message never landed, and the code is
       * what separates a rejection the session caused from one the recipient caused.
       */
      errorCode?: string
    }

export type SessionEventType = SessionEvent['type']
export type SessionEventListener = (event: SessionEvent) => void

export class EventBus {
  private readonly listeners = new Set<SessionEventListener>()

  constructor(private readonly logger: Logger) {}

  subscribe(listener: SessionEventListener): () => void {
    this.listeners.add(listener)
    return () => {
      this.listeners.delete(listener)
    }
  }

  get listenerCount(): number {
    return this.listeners.size
  }

  emit(event: SessionEvent): void {
    for (const listener of this.listeners) {
      try {
        listener(event)
      } catch (err) {
        this.logger.error(
          { err, sessionId: event.sessionId, eventType: event.type },
          'event listener threw, continuing',
        )
      }
    }
  }
}

export const nowIso = (): string => new Date().toISOString()
