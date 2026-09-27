import type { AppInstance } from '../app.js'
import type { Pool } from '../db.js'
import type { MetricsRegistry } from '../metrics/registry.js'
import { gauge } from '../metrics/registry.js'
import type { SessionManager } from '../sessions/manager.js'
import { listSessions, type SessionStatus } from '../sessions/repository.js'

const ALL_STATUSES: SessionStatus[] = [
  'new',
  'pairing',
  'connected',
  'disconnected',
  'logged_out',
  'unhealthy',
]

export interface MetricsDeps {
  pool: Pool
  sessions: SessionManager
  metrics: MetricsRegistry
  startedAt: number
}

export function describeMetrics(metrics: MetricsRegistry): void {
  metrics.describe('baileys_uptime_seconds', 'Seconds since the process started', 'gauge')
  metrics.describe('baileys_sessions_total', 'Sessions known to the database', 'gauge')
  metrics.describe('baileys_session_up', 'One when the session socket is open', 'gauge')
  metrics.describe('baileys_session_status', 'One for the session current status', 'gauge')
  metrics.describe(
    'baileys_session_consecutive_send_failures',
    'Consecutive session-fault send failures; drives the unhealthy status',
    'gauge',
  )
  metrics.describe(
    'baileys_session_reconnect_attempts',
    'Reconnect attempts since the session last connected',
    'gauge',
  )
  metrics.describe(
    'baileys_session_next_reconnect_seconds',
    'Seconds until the scheduled reconnect; absent when none is pending',
    'gauge',
  )
  metrics.describe(
    'baileys_session_last_connected_seconds',
    'Seconds since the session last connected',
    'gauge',
  )
  metrics.describe(
    'baileys_session_last_successful_send_seconds',
    'Seconds since the session last sent successfully',
    'gauge',
  )
  metrics.describe('baileys_sends_total', 'Send attempts by outcome', 'counter')
  metrics.describe('baileys_send_failures_total', 'Failed sends by error code', 'counter')
  metrics.describe('baileys_reconnects_total', 'Reconnect attempts started', 'counter')
  metrics.describe('baileys_webhook_deliveries_total', 'Webhook deliveries by outcome', 'counter')
  metrics.describe(
    'baileys_messages_stored_total',
    'Rows in wa_sent_messages by status, surviving restarts',
    'gauge',
  )
}

export function registerMetricsRoute(app: AppInstance, deps: MetricsDeps): void {
  const { pool, sessions, metrics, startedAt } = deps

  app.get('/metrics', async (_req, reply) => {
    const rows = await listSessions(pool)
    const now = Date.now()
    const gauges = [
      gauge('baileys_uptime_seconds', {}, Math.round((now - startedAt) / 1000)),
      gauge('baileys_sessions_total', {}, rows.length),
    ]

    for (const row of rows) {
      const live = sessions.snapshot(row.id)
      const labels = { session_id: row.id, label: row.label }
      const status = live?.status ?? row.status

      gauges.push(gauge('baileys_session_up', labels, live?.socketConnected ? 1 : 0))

      for (const candidate of ALL_STATUSES) {
        gauges.push(
          gauge(
            'baileys_session_status',
            { ...labels, status: candidate },
            status === candidate ? 1 : 0,
          ),
        )
      }

      gauges.push(
        gauge('baileys_session_consecutive_send_failures', labels, live?.consecutiveFailures ?? 0),
        gauge('baileys_session_reconnect_attempts', labels, live?.reconnectAttempts ?? 0),
      )

      if (live?.nextReconnectInMs !== undefined) {
        gauges.push(
          gauge('baileys_session_next_reconnect_seconds', labels, live.nextReconnectInMs / 1000),
        )
      }
      if (live?.lastConnectedAt) {
        gauges.push(
          gauge(
            'baileys_session_last_connected_seconds',
            labels,
            Math.round((now - live.lastConnectedAt.getTime()) / 1000),
          ),
        )
      }
      if (live?.lastSuccessfulSendAt) {
        gauges.push(
          gauge(
            'baileys_session_last_successful_send_seconds',
            labels,
            Math.round((now - live.lastSuccessfulSendAt.getTime()) / 1000),
          ),
        )
      }
    }

    const stored = await pool.query<{ session_id: string; status: string; error_code: string | null; n: string }>(
      `SELECT session_id, status, error_code, count(*)::text AS n
       FROM wa_sent_messages GROUP BY session_id, status, error_code`,
    )
    for (const row of stored.rows) {
      gauges.push(
        gauge(
          'baileys_messages_stored_total',
          {
            session_id: row.session_id,
            status: row.status,
            error_code: row.error_code ?? '',
          },
          Number(row.n),
        ),
      )
    }

    return reply
      .header('Content-Type', 'text/plain; version=0.0.4; charset=utf-8')
      .send(metrics.render(gauges))
  })
}
