import type { Config } from '../config.js'
import type { Logger } from '../logger.js'
import type { EventBus, SessionEvent, SessionEventType } from '../sessions/events.js'
import { EVENT_HEADER, SIGNATURE_HEADER, TIMESTAMP_HEADER, signPayload } from './signature.js'

const DELIVERED: ReadonlySet<SessionEventType> = new Set([
  'message.received',
  'message.status',
  'session.status_changed',
])

export interface WebhookDispatcherOptions {
  url: string
  secret: string
  timeoutMs: number
  logger: Logger
  fetchImpl?: typeof fetch
}

export class WebhookDispatcher {
  private readonly fetchImpl: typeof fetch
  private inFlight = 0

  constructor(private readonly options: WebhookDispatcherOptions) {
    this.fetchImpl = options.fetchImpl ?? fetch
  }

  attach(events: EventBus): () => void {
    return events.subscribe((event) => {
      if (!DELIVERED.has(event.type)) return
      void this.deliver(event)
    })
  }

  get pending(): number {
    return this.inFlight
  }

  async deliver(event: SessionEvent): Promise<void> {
    const { url, secret, timeoutMs, logger } = this.options
    const body = JSON.stringify(event)
    const timestamp = Date.now()

    this.inFlight += 1
    try {
      const response = await this.fetchImpl(url, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          [EVENT_HEADER]: event.type,
          [TIMESTAMP_HEADER]: String(timestamp),
          [SIGNATURE_HEADER]: `sha256=${signPayload(secret, timestamp, body)}`,
        },
        body,
        signal: AbortSignal.timeout(timeoutMs),
      })

      if (!response.ok) {
        logger.warn(
          { sessionId: event.sessionId, eventType: event.type, status: response.status },
          'webhook rejected by consumer',
        )
        return
      }
      logger.debug({ sessionId: event.sessionId, eventType: event.type }, 'webhook delivered')
    } catch (err) {
      logger.warn(
        { sessionId: event.sessionId, eventType: event.type, err },
        'webhook delivery failed, not retrying',
      )
    } finally {
      this.inFlight -= 1
    }
  }
}

export function attachWebhooks(
  config: Config,
  events: EventBus,
  logger: Logger,
): { dispatcher: WebhookDispatcher | undefined; detach: () => void } {
  if (!config.WEBHOOK_URL || !config.WEBHOOK_SECRET) {
    logger.info('webhooks disabled (WEBHOOK_URL not set)')
    return { dispatcher: undefined, detach: () => {} }
  }

  const dispatcher = new WebhookDispatcher({
    url: config.WEBHOOK_URL,
    secret: config.WEBHOOK_SECRET,
    timeoutMs: config.WEBHOOK_TIMEOUT_MS,
    logger,
  })
  logger.info({ url: config.WEBHOOK_URL, timeoutMs: config.WEBHOOK_TIMEOUT_MS }, 'webhooks enabled')
  return { dispatcher, detach: dispatcher.attach(events) }
}
