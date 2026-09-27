import type { AppInstance } from '../app.js'
import type { Pool } from '../db.js'
import { sessionNotFound } from '../errors.js'
import type { SessionManager } from '../sessions/manager.js'
import { getSession } from '../sessions/repository.js'

const SSE_HEADERS = {
  'Content-Type': 'text/event-stream; charset=utf-8',
  'Cache-Control': 'no-cache, no-transform',
  Connection: 'keep-alive',
  'X-Accel-Buffering': 'no',
} as const

const HEARTBEAT_MS = 20_000

function formatEvent(event: string, data: unknown): string {
  return `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`
}

export interface QrStreamDeps {
  pool: Pool
  sessions: SessionManager
}

export function registerQrStreamRoute(app: AppInstance, deps: QrStreamDeps): void {
  app.get<{ Params: { id: string } }>('/sessions/:id/qr/stream', async (req, reply) => {
    const { id } = req.params
    const row = await getSession(deps.pool, id)
    if (!row) throw sessionNotFound(id)

    reply.hijack()
    const res = reply.raw
    res.writeHead(200, SSE_HEADERS)

    const log = req.log.child({ sessionId: id })
    let closed = false

    const send = (event: string, data: unknown): void => {
      if (closed || res.writableEnded) return
      try {
        res.write(formatEvent(event, data))
      } catch (err) {
        log.warn({ err }, 'qr stream write failed, closing')
        cleanup()
      }
    }

    const live = deps.sessions.snapshot(id)

    send('status', { id, status: live?.status ?? row.status })
    if (live?.qr) send('qr', { id, qr: live.qr })

    const unsubscribe = deps.sessions.events.subscribe((event) => {
      if (event.sessionId !== id) return

      if (event.type === 'qr') {
        send('qr', { id, qr: event.qr })
        return
      }

      if (event.type === 'session.status_changed') {
        send('status', { id, status: event.status, phoneNumber: event.phoneNumber })

        if (event.status === 'connected') {
          send('paired', { id, phoneNumber: event.phoneNumber })
          cleanup()
        } else if (event.status === 'logged_out') {
          send('failed', { id, reason: 'logged_out' })
          cleanup()
        }
      }
    })

    const heartbeat = setInterval(() => {
      if (closed || res.writableEnded) return
      res.write(': ping\n\n')
    }, HEARTBEAT_MS)
    heartbeat.unref()

    function cleanup(): void {
      if (closed) return
      closed = true
      clearInterval(heartbeat)
      unsubscribe()
      if (!res.writableEnded) res.end()
      log.info('qr stream closed')
    }

    req.raw.on('close', cleanup)
    req.raw.on('error', cleanup)

    log.info({ subscribers: deps.sessions.events.listenerCount }, 'qr stream opened')
  })
}
