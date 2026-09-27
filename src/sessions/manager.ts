import {
  Browsers,
  delay,
  DisconnectReason,
  fetchLatestBaileysVersion,
  makeWASocket,
  type ConnectionState,
  type WASocket,
} from 'baileys'
import type { Pool } from '../db.js'
import { sessionLoggedOut, sessionNotConnected, sessionNotFound, upstreamTimeout } from '../errors.js'
import type { Logger } from '../logger.js'
import { MetricsRegistry } from '../metrics/registry.js'
import { clearAuthState, isPairingIncomplete, usePostgresAuthState } from '../wa/auth-state.js'
import { statusCodeOf } from '../wa/error-mapping.js'
import { WA_TIMEOUTS, withTimeout } from '../wa/timeout.js'
import { EventBus, nowIso } from './events.js'
import { subscribeToMessageEvents } from './message-events.js'
import {
  deleteSession,
  getSession,
  listRestorableSessions,
  setSessionConnected,
  setSessionStatus,
  type SessionStatus,
} from './repository.js'

const RECONNECT_BASE_MS = 2_000
const RECONNECT_MAX_MS = 5 * 60_000
const MAX_RECONNECT_ATTEMPTS = 10

const RESTART_DELAY_MS = 250
const MAX_RESTARTS = 5

const RESTORE_STAGGER_MS = 3_000

const UNHEALTHY_AFTER_FAILURES = 5

const SESSION_FAULT_CODES: ReadonlySet<string> = new Set([
  'send_failed',
  'session_not_connected',
  'rate_limited_by_wa',
])

export interface ManagedSession {
  id: string
  status: SessionStatus
  qr: string | undefined
  phoneNumber: string | undefined
  lastConnectedAt: Date | undefined
  reconnectAttempts: number
  socket: WASocket | undefined
}

export interface SessionSnapshot {
  id: string
  status: SessionStatus
  qr: string | undefined
  phoneNumber: string | undefined
  lastConnectedAt: Date | undefined
  reconnectAttempts: number
  socketConnected: boolean
  lastSuccessfulSendAt: Date | undefined
  lastFailedSendAt: Date | undefined
  consecutiveFailures: number
  lastErrorCode: string | undefined
  nextReconnectInMs: number | undefined
}

interface Entry extends ManagedSession {
  restarts: number
  reconnectTimer: NodeJS.Timeout | undefined
  closing: boolean
  pendingCreds: Promise<void>
  lastSuccessfulSendAt: Date | undefined
  lastFailedSendAt: Date | undefined
  consecutiveFailures: number
  lastErrorCode: string | undefined
  reconnectDueAt: number | undefined
}

export function isolate(fn: () => void | Promise<void>, onError: (err: unknown) => void): void {
  try {
    const result = fn()
    if (result instanceof Promise) result.catch(onError)
  } catch (err) {
    onError(err)
  }
}

export class SessionManager {
  private readonly entries = new Map<string, Entry>()
  private version: [number, number, number] | undefined
  private shuttingDown = false
  private restoring: Promise<void> | undefined

  constructor(
    private readonly pool: Pool,
    private readonly logger: Logger,
    readonly events: EventBus = new EventBus(logger),
    private readonly metrics: MetricsRegistry = new MetricsRegistry(),
  ) {}

  snapshot(id: string): SessionSnapshot | undefined {
    const entry = this.entries.get(id)
    if (!entry) return undefined
    return {
      id: entry.id,
      status: entry.status,
      qr: entry.qr,
      phoneNumber: entry.phoneNumber,
      lastConnectedAt: entry.lastConnectedAt,
      reconnectAttempts: entry.reconnectAttempts,
      socketConnected: entry.socket !== undefined && entry.status !== 'disconnected',
      lastSuccessfulSendAt: entry.lastSuccessfulSendAt,
      lastFailedSendAt: entry.lastFailedSendAt,
      consecutiveFailures: entry.consecutiveFailures,
      lastErrorCode: entry.lastErrorCode,
      nextReconnectInMs: entry.reconnectDueAt
        ? Math.max(0, entry.reconnectDueAt - Date.now())
        : undefined,
    }
  }

  snapshotAll(): SessionSnapshot[] {
    return [...this.entries.keys()].map((id) => this.snapshot(id)!)
  }

  recordSendOutcome(id: string, outcome: { ok: true } | { ok: false; errorCode: string }): void {
    const entry = this.entries.get(id)
    if (!entry) return

    if (outcome.ok) {
      entry.lastSuccessfulSendAt = new Date()
      entry.consecutiveFailures = 0
      entry.lastErrorCode = undefined
      // One success is enough to say the session works again.
      if (entry.status === 'unhealthy') {
        void this.setStatus(entry, 'connected').catch((err) =>
          this.logger.error({ sessionId: id, err }, 'failed to clear unhealthy status'),
        )
      }
      return
    }

    entry.lastFailedSendAt = new Date()
    entry.lastErrorCode = outcome.errorCode

    if (!SESSION_FAULT_CODES.has(outcome.errorCode)) return

    entry.consecutiveFailures += 1
    if (entry.consecutiveFailures >= UNHEALTHY_AFTER_FAILURES && entry.status === 'connected') {
      this.logger.error(
        { sessionId: id, consecutiveFailures: entry.consecutiveFailures, errorCode: outcome.errorCode },
        'session marked unhealthy; the worker should pause its queue',
      )
      void this.setStatus(entry, 'unhealthy').catch((err) =>
        this.logger.error({ sessionId: id, err }, 'failed to set unhealthy status'),
      )
    }
  }

  async waitForPairingReady(id: string, timeoutMs = 20_000): Promise<void> {
    const entry = this.entries.get(id)
    if (!entry) throw sessionNotConnected(id, 'not started')
    if (entry.status === 'connected' || entry.qr) return

    await new Promise<void>((resolve, reject) => {
      let unsubscribe = (): void => {}
      const timer = setTimeout(() => {
        unsubscribe()
        reject(upstreamTimeout('pairing socket readiness', timeoutMs))
      }, timeoutMs)
      timer.unref()

      const finish = (fn: () => void): void => {
        clearTimeout(timer)
        unsubscribe()
        fn()
      }

      unsubscribe = this.events.subscribe((event) => {
        if (event.sessionId !== id) return
        if (event.type === 'qr') {
          finish(resolve)
        } else if (event.type === 'session.status_changed') {
          if (event.status === 'connected') finish(resolve)
          else if (event.status === 'logged_out') finish(() => reject(sessionLoggedOut(id)))
        }
      })
    })
  }

  async restart(id: string): Promise<SessionSnapshot> {
    const row = await getSession(this.pool, id)
    if (!row) throw sessionNotFound(id)
    if (row.status === 'logged_out') throw sessionLoggedOut(id)

    const entry = this.entries.get(id)
    if (entry) {
      clearTimeout(entry.reconnectTimer)
      entry.reconnectTimer = undefined
      entry.reconnectDueAt = undefined
      entry.closing = true
      try {
        entry.socket?.end(undefined)
      } catch (err) {
        this.logger.child({ sessionId: id }).warn({ err }, 'error closing socket for restart')
      }
      entry.socket = undefined
      entry.qr = undefined
      entry.reconnectAttempts = 0
      entry.restarts = 0
      entry.consecutiveFailures = 0
      entry.closing = false
      await this.setStatus(entry, 'disconnected')
      this.logger.info({ sessionId: id }, 'restarting session socket')
      await this.openSocket(entry)
      return this.snapshot(id)!
    }

    return this.connect(id)
  }

  requireConnectedSocket(id: string): WASocket {
    const entry = this.entries.get(id)
    const usable = entry?.status === 'connected' || entry?.status === 'unhealthy'
    if (!entry?.socket || !usable) {
      if (entry?.status === 'logged_out') throw sessionLoggedOut(id)
      throw sessionNotConnected(id, entry?.status ?? 'not started')
    }
    return entry.socket
  }

  requirePairingSocket(id: string): WASocket {
    const entry = this.entries.get(id)
    if (!entry?.socket) throw sessionNotConnected(id, entry?.status ?? 'not started')
    if (entry.status === 'logged_out') throw sessionLoggedOut(id)
    return entry.socket
  }

  async connect(id: string): Promise<SessionSnapshot> {
    if (this.shuttingDown) throw sessionNotConnected(id, 'shutting down')

    const row = await getSession(this.pool, id)
    if (!row) throw sessionNotFound(id)
    if (row.status === 'logged_out') throw sessionLoggedOut(id)

    const existing = this.entries.get(id)
    if (existing?.socket) return this.snapshot(id)!

    const entry: Entry = existing ?? {
      id,
      status: initialLiveStatus(row.status),
      qr: undefined,
      phoneNumber: row.phone_number ?? undefined,
      lastConnectedAt: undefined,
      reconnectAttempts: 0,
      socket: undefined,
      restarts: 0,
      reconnectTimer: undefined,
      closing: false,
      pendingCreds: Promise.resolve(),
      lastSuccessfulSendAt: undefined,
      lastFailedSendAt: undefined,
      consecutiveFailures: 0,
      lastErrorCode: undefined,
      reconnectDueAt: undefined,
    }
    entry.closing = false
    this.entries.set(id, entry)

    await this.openSocket(entry)
    return this.snapshot(id)!
  }

  async restoreAll(): Promise<void> {
    this.restoring = this.doRestoreAll()
    return this.restoring
  }

  private async doRestoreAll(): Promise<void> {
    const rows = await listRestorableSessions(this.pool)
    if (rows.length === 0) {
      this.logger.info('no sessions to restore')
      return
    }

    this.logger.info(
      { count: rows.length, staggerMs: RESTORE_STAGGER_MS },
      'restoring sessions from postgres',
    )

    for (const [index, row] of rows.entries()) {
      if (this.shuttingDown) {
        this.logger.info({ restored: index }, 'shutdown during restore, stopping')
        return
      }
      if (index > 0) await delay(RESTORE_STAGGER_MS)

      try {
        await this.connect(row.id)
        this.logger.info({ sessionId: row.id, index: index + 1, of: rows.length }, 'restored')
      } catch (err) {
        this.logger.error({ sessionId: row.id, err }, 'failed to restore session, continuing')
      }
    }
  }

  private async openSocket(entry: Entry): Promise<void> {
    if (!this.version) {
      const { version } = await withTimeout(
        'fetchLatestBaileysVersion',
        WA_TIMEOUTS.fetchVersion,
        fetchLatestBaileysVersion(),
      )
      this.version = version
      this.logger.info({ version }, 'resolved whatsapp web version')
    }

    const log = this.logger.child({ sessionId: entry.id })

    let auth = await usePostgresAuthState(this.pool, entry.id, log)
    if (isPairingIncomplete(auth.state.creds)) {
      log.warn('incomplete pairing creds found; clearing so the session can pair again')
      await clearAuthState(this.pool, entry.id)
      auth = await usePostgresAuthState(this.pool, entry.id, log)
    }
    const { state, saveCreds } = auth

    const socket = makeWASocket({
      version: this.version,
      auth: state,
      logger: log,
      browser: Browsers.ubuntu('Chrome'),
      qrTimeout: 120_000,
      markOnlineOnConnect: false,
    })
    entry.socket = socket

    socket.ev.on('creds.update', () => {
      isolate(
        () => {
          entry.pendingCreds = entry.pendingCreds
            .then(() => saveCreds())
            .catch((err) => log.error({ err }, 'failed to persist creds'))
        },
        (err) => log.error({ err }, 'error queueing creds write'),
      )
    })

    socket.ev.on('connection.update', (update) => {
      isolate(
        () => this.onConnectionUpdate(entry, update),
        (err) => log.error({ err }, 'error handling connection update'),
      )
    })

    isolate(
      () => subscribeToMessageEvents(socket, entry.id, this.events, log),
      (err) => log.error({ err }, 'failed to subscribe to message events'),
    )
  }

  private async onConnectionUpdate(
    entry: Entry,
    update: Partial<ConnectionState>,
  ): Promise<void> {
    const { connection, qr, lastDisconnect } = update
    const log = this.logger.child({ sessionId: entry.id })

    if (qr) {
      entry.qr = qr
      await this.setStatus(entry, 'pairing')
      this.events.emit({ type: 'qr', sessionId: entry.id, at: nowIso(), qr })
      log.info('qr issued')
    }

    if (connection === 'open') {
      entry.qr = undefined
      entry.reconnectAttempts = 0
      entry.restarts = 0
      entry.lastConnectedAt = new Date()
      entry.phoneNumber = entry.socket?.user?.id?.split(':')[0]?.split('@')[0]
      entry.status = 'connected'
      if (entry.phoneNumber) {
        await setSessionConnected(this.pool, entry.id, entry.phoneNumber)
      } else {
        await setSessionStatus(this.pool, entry.id, 'connected')
      }
      this.emitStatus(entry)
      log.info({ phoneNumber: entry.phoneNumber }, 'session connected')
      return
    }

    if (connection !== 'close') return

    const status = statusCodeOf(lastDisconnect?.error)
    entry.socket = undefined

    if (entry.closing || this.shuttingDown) {
      log.info('socket closed on request')
      return
    }

    if (status === DisconnectReason.loggedOut) {
      entry.qr = undefined
      await this.setStatus(entry, 'logged_out')
      await clearAuthState(this.pool, entry.id)
      log.warn('session logged out; auth state cleared, not reconnecting')
      return
    }

    if (status === DisconnectReason.restartRequired) {
      if (entry.restarts >= MAX_RESTARTS) {
        await this.setStatus(entry, 'disconnected')
        log.error({ restarts: entry.restarts }, 'restart loop, giving up')
        return
      }
      entry.restarts += 1
      log.info({ restarts: entry.restarts }, 'restart required (515), reconnecting immediately')
      this.scheduleReconnect(entry, RESTART_DELAY_MS)
      return
    }

    await this.setStatus(entry, 'disconnected')

    if (entry.reconnectAttempts >= MAX_RECONNECT_ATTEMPTS) {
      log.error(
        { attempts: entry.reconnectAttempts, status },
        'reconnect budget exhausted, manual action required',
      )
      return
    }

    entry.reconnectAttempts += 1
    const delay = backoffDelay(entry.reconnectAttempts)
    log.warn({ status, attempt: entry.reconnectAttempts, delay }, 'disconnected, reconnecting')
    this.scheduleReconnect(entry, delay)
  }

  private scheduleReconnect(entry: Entry, delayMs: number): void {
    clearTimeout(entry.reconnectTimer)
    this.metrics.increment('baileys_reconnects_total', { session_id: entry.id })
    entry.reconnectDueAt = Date.now() + delayMs
    entry.reconnectTimer = setTimeout(() => {
      entry.reconnectTimer = undefined
      entry.reconnectDueAt = undefined
      if (entry.closing || this.shuttingDown) return
      void this.openSocket(entry).catch((err) => {
        this.logger.child({ sessionId: entry.id }).error({ err }, 'reconnect failed')
      })
    }, delayMs)
    entry.reconnectTimer.unref()
  }

  private async setStatus(entry: Entry, status: SessionStatus): Promise<void> {
    if (entry.status === status) return
    entry.status = status
    await setSessionStatus(this.pool, entry.id, status)
    this.emitStatus(entry)
  }

  private emitStatus(entry: Entry): void {
    this.events.emit({
      type: 'session.status_changed',
      sessionId: entry.id,
      at: nowIso(),
      status: entry.status,
      phoneNumber: entry.phoneNumber,
    })
  }

  async reset(id: string): Promise<void> {
    const row = await getSession(this.pool, id)
    if (!row) throw sessionNotFound(id)

    const entry = this.entries.get(id)
    if (entry) {
      clearTimeout(entry.reconnectTimer)
      entry.reconnectTimer = undefined
      entry.closing = true
      try {
        entry.socket?.end(undefined)
      } catch (err) {
        this.logger.child({ sessionId: id }).warn({ err }, 'error closing socket for reset')
      }
      this.entries.delete(id)
    }

    await clearAuthState(this.pool, id)
    await setSessionStatus(this.pool, id, 'new')
    this.logger.info({ sessionId: id }, 'session reset to new; pair it again')
  }

  async logout(id: string): Promise<void> {
    const row = await getSession(this.pool, id)
    if (!row) throw sessionNotFound(id)

    const entry = this.entries.get(id)
    if (entry) {
      entry.closing = true
      clearTimeout(entry.reconnectTimer)
      entry.reconnectTimer = undefined
      entry.qr = undefined
      try {
        const pending = entry.socket?.logout()
        if (pending) await withTimeout('logout', WA_TIMEOUTS.logout, pending)
      } catch (err) {
        this.logger.child({ sessionId: id }).warn({ err }, 'logout call failed, clearing anyway')
      }
      entry.socket = undefined
      entry.status = 'logged_out'
      this.emitStatus(entry)
    }

    await setSessionStatus(this.pool, id, 'logged_out')
    await clearAuthState(this.pool, id)
  }

  async delete(id: string): Promise<void> {
    const entry = this.entries.get(id)
    if (entry) {
      clearTimeout(entry.reconnectTimer)
      entry.reconnectTimer = undefined
      entry.closing = true
      try {
        entry.socket?.end(undefined)
      } catch (err) {
        this.logger.child({ sessionId: id }).warn({ err }, 'error closing socket for delete')
      }
      this.entries.delete(id)
    }

    await deleteSession(this.pool, id)
  }

  async shutdown(): Promise<void> {
    this.shuttingDown = true

    if (this.restoring) await this.restoring.catch(() => {})

    const entries = [...this.entries.values()]

    for (const entry of entries) {
      clearTimeout(entry.reconnectTimer)
      entry.reconnectTimer = undefined
      entry.closing = true
      try {
        entry.socket?.end(undefined)
      } catch (err) {
        this.logger.child({ sessionId: entry.id }).warn({ err }, 'error closing socket')
      }
      entry.socket = undefined
    }

    const flushes = await Promise.allSettled(entries.map((entry) => entry.pendingCreds))
    const failed = flushes.filter((r) => r.status === 'rejected').length
    if (failed > 0) this.logger.error({ failed }, 'some creds writes did not flush')

    this.logger.info({ sessions: entries.length }, 'all sockets closed, creds flushed')
    this.entries.clear()
  }
}

export function initialLiveStatus(stored: SessionStatus): SessionStatus {
  return stored === 'connected' || stored === 'unhealthy' ? 'disconnected' : stored
}

export function backoffDelay(attempt: number): number {
  const exponential = Math.min(RECONNECT_BASE_MS * 2 ** (attempt - 1), RECONNECT_MAX_MS)
  return Math.round(exponential * (0.8 + Math.random() * 0.4))
}

export const RECONNECT_LIMITS = { RECONNECT_BASE_MS, RECONNECT_MAX_MS, MAX_RECONNECT_ATTEMPTS }
