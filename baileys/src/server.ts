import Fastify, { type FastifyError } from 'fastify'
import type { AppInstance } from './app.js'
import { credentialsFrom, registerAuth } from './auth.js'
import type { Config } from './config.js'
import type { Pool } from './db.js'
import {
  ApiError,
  invalidPayload,
  notFound,
  payloadTooLarge,
  unsupportedMediaType,
} from './errors.js'
import type { Logger } from './logger.js'
import { MetricsRegistry } from './metrics/registry.js'
import { registerHealthRoutes } from './routes/health.js'
import { describeMetrics, registerMetricsRoute } from './routes/metrics.js'
import { registerQrStreamRoute } from './routes/qr-stream.js'
import { registerSessionRoutes } from './routes/sessions.js'
import { isWhatsAppError, mapWhatsAppError } from './wa/error-mapping.js'
import type { SessionManager } from './sessions/manager.js'

export interface BuildServerOptions {
  config: Config
  pool: Pool
  logger: Logger
  sessions: SessionManager
  metrics?: MetricsRegistry
  startedAt?: number
}

function toApiError(err: FastifyError, path: string): ApiError {
  if (err instanceof ApiError) return err

  if (isWhatsAppError(err)) {
    const sessionId = path.split('/')[2] ?? 'unknown'
    return mapWhatsAppError(err, sessionId)
  }

  const status = err.statusCode ?? 500
  switch (status) {
    case 413:
      return payloadTooLarge(err.message)
    case 415:
      return unsupportedMediaType(err.message)
    default:
      break
  }
  if (status >= 400 && status < 500) return invalidPayload(err.message)
  return new ApiError(status, 'internal_error', 'internal error', true)
}

export function buildServer({
  config,
  pool,
  logger,
  sessions,
  metrics = new MetricsRegistry(),
  startedAt = Date.now(),
}: BuildServerOptions): AppInstance {
  describeMetrics(metrics)

  const app: AppInstance = Fastify({
    loggerInstance: logger,
    trustProxy: true,
    bodyLimit: 1024 * 1024,
    requestTimeout: 30_000,
  })

  registerAuth(app, credentialsFrom(config))
  registerHealthRoutes(app, pool, startedAt)
  registerSessionRoutes(app, { pool, sessions, logger, metrics })
  registerQrStreamRoute(app, { pool, sessions })
  registerMetricsRoute(app, { pool, sessions, metrics, startedAt })

  app.setNotFoundHandler(async (_req, reply) => {
    const err = notFound()
    return reply.code(err.statusCode).send(err.toBody())
  })

  app.setErrorHandler(async (err: FastifyError, req, reply) => {
    const mapped = toApiError(err, req.url)
    if (mapped.statusCode >= 500) {
      req.log.error({ err, errorCode: mapped.errorCode }, 'unhandled error')
    }
    return reply.code(mapped.statusCode).send(mapped.toBody())
  })

  return app
}
