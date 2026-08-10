import { z } from 'zod'
import type { AppInstance } from '../app.js'
import type { Pool } from '../db.js'
import { invalidPayload, sessionNotFound } from '../errors.js'
import type { Logger } from '../logger.js'
import type { MetricsRegistry } from '../metrics/registry.js'
import { sendTextMessage } from '../messages/send.js'
import type { SessionManager } from '../sessions/manager.js'
import {
  createSession,
  getSession,
  listSessions,
  type SessionRow,
} from '../sessions/repository.js'
import { mapPairingError } from '../wa/error-mapping.js'
import { InvalidPhoneNumberError, normalisePhoneNumber } from '../wa/phone.js'
import { WA_TIMEOUTS, withTimeout } from '../wa/timeout.js'

const createBody = z.object({
  label: z.string().trim().min(1, 'label is required').max(200),
})

const pairBody = z.object({
  phoneNumber: z.string().min(1, 'phoneNumber is required'),
})

const sendBody = z.object({
  idempotencyKey: z.string().trim().min(1).max(255),
  to: z.string().min(1),
  type: z.literal('text'),
  text: z.string().min(1).max(65536),
})

const idParams = z.object({ id: z.string().min(1) })

/** Turns a zod failure into the contract's `invalid_payload`, never a bare 400 from Fastify. */
function parse<T>(schema: z.ZodType<T>, value: unknown): T {
  const result = schema.safeParse(value)
  if (result.success) return result.data
  const detail = result.error.issues
    .map((issue) => `${issue.path.join('.') || 'body'}: ${issue.message}`)
    .join('; ')
  throw invalidPayload(detail)
}

interface SessionView {
  id: string
  label: string
  status: string
  phoneNumber: string | null
  hasQr: boolean
  createdAt: string
  updatedAt: string
  /** Per-session health, so the worker can pace without reading logs. */
  health: {
    socketConnected: boolean
    lastConnectedAt: string | null
    lastSuccessfulSendAt: string | null
    lastFailedSendAt: string | null
    consecutiveFailures: number
    lastErrorCode: string | null
    reconnectAttempts: number
    nextReconnectInMs: number | null
  }
}

/**
 * The database row is the durable record; the manager knows what the socket is doing right
 * now. A session marked `connected` in Postgres whose socket died a second ago is still
 * `connected` there, so live state wins when the two disagree.
 */
function toView(row: SessionRow, sessions: SessionManager): SessionView {
  const live = sessions.snapshot(row.id)
  return {
    id: row.id,
    label: row.label,
    status: live?.status ?? row.status,
    phoneNumber: live?.phoneNumber ?? row.phone_number,
    hasQr: Boolean(live?.qr),
    createdAt: row.created_at.toISOString(),
    updatedAt: row.updated_at.toISOString(),
    health: {
      socketConnected: live?.socketConnected ?? false,
      lastConnectedAt: live?.lastConnectedAt?.toISOString() ?? null,
      lastSuccessfulSendAt: live?.lastSuccessfulSendAt?.toISOString() ?? null,
      lastFailedSendAt: live?.lastFailedSendAt?.toISOString() ?? null,
      consecutiveFailures: live?.consecutiveFailures ?? 0,
      lastErrorCode: live?.lastErrorCode ?? null,
      reconnectAttempts: live?.reconnectAttempts ?? 0,
      nextReconnectInMs: live?.nextReconnectInMs ?? null,
    },
  }
}

export interface SessionRouteDeps {
  pool: Pool
  sessions: SessionManager
  logger: Logger
  metrics?: MetricsRegistry
}

export function registerSessionRoutes(app: AppInstance, deps: SessionRouteDeps): void {
  const { pool, sessions } = deps

  async function loadSession(id: string): Promise<SessionRow> {
    const row = await getSession(pool, id)
    if (!row) throw sessionNotFound(id)
    return row
  }

  app.post('/sessions', async (req, reply) => {
    const { label } = parse(createBody, req.body)
    const row = await createSession(pool, label)
    return reply.code(201).send(toView(row, sessions))
  })

  app.get('/sessions', async () => {
    const rows = await listSessions(pool)
    return { sessions: rows.map((row) => toView(row, sessions)) }
  })

  app.get('/sessions/:id', async (req) => {
    const { id } = parse(idParams, req.params)
    return toView(await loadSession(id), sessions)
  })

  app.post('/sessions/:id/connect', async (req) => {
    const { id } = parse(idParams, req.params)
    await loadSession(id)
    const snapshot = await sessions.connect(id)
    return { id, status: snapshot.status }
  })

  app.post('/sessions/:id/pair', async (req) => {
    const { id } = parse(idParams, req.params)
    const { phoneNumber } = parse(pairBody, req.body)
    await loadSession(id)

    let digits: string
    try {
      digits = normalisePhoneNumber(phoneNumber)
    } catch (err) {
      if (err instanceof InvalidPhoneNumberError) throw invalidPayload(err.message)
      throw err
    }

    await sessions.connect(id)
    await sessions.waitForPairingReady(id)
    const socket = sessions.requirePairingSocket(id)

    try {
      const code = await withTimeout(
        'requestPairingCode',
        WA_TIMEOUTS.requestPairingCode,
        socket.requestPairingCode(digits),
      )
      return { id, phoneNumber: digits, pairingCode: code }
    } catch (err) {
      throw mapPairingError(err, id)
    }
  })

  app.get('/sessions/:id/qr', async (req) => {
    const { id } = parse(idParams, req.params)
    const row = await loadSession(id)
    const live = sessions.snapshot(id)
    return { id, status: live?.status ?? row.status, qr: live?.qr ?? null }
  })

  app.post('/sessions/:id/restart', async (req) => {
    const { id } = parse(idParams, req.params)
    await loadSession(id)
    const snapshot = await sessions.restart(id)
    return { id, status: snapshot.status }
  })

  app.post('/sessions/:id/reset', async (req) => {
    const { id } = parse(idParams, req.params)
    await loadSession(id)
    await sessions.reset(id)
    return { id, status: 'new' }
  })

  app.post('/sessions/:id/logout', async (req) => {
    const { id } = parse(idParams, req.params)
    await loadSession(id)
    await sessions.logout(id)
    return { id, status: 'logged_out' }
  })

  app.post('/sessions/:id/send', async (req, reply) => {
    const { id } = parse(idParams, req.params)
    const body = parse(sendBody, req.body)
    await loadSession(id)

    const result = await sendTextMessage(deps, id, body)
    return reply.code(200).send(result)
  })
}
