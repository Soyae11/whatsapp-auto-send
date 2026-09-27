import { loadConfig } from './config.js'
import { createPool } from './db.js'
import { createLogger } from './logger.js'
import { runMigrations } from './migrate.js'
import { MIGRATIONS_DIR } from './paths.js'
import { buildServer } from './server.js'
import { SessionManager } from './sessions/manager.js'
import { attachWebhooks } from './webhooks/dispatcher.js'

async function main(): Promise<void> {
  const startedAt = Date.now()

  let config
  try {
    config = loadConfig()
  } catch (err) {
    console.error((err as Error).message)
    process.exit(1)
  }

  const logger = createLogger(config.LOG_LEVEL)
  const pool = createPool(config.DATABASE_URL, logger)

  try {
    await runMigrations(pool, MIGRATIONS_DIR, logger)
  } catch (err) {
    logger.fatal({ err }, 'migrations failed')
    await pool.end().catch(() => {})
    process.exit(1)
  }

  const sessions = new SessionManager(pool, logger)
  attachWebhooks(config, sessions.events, logger)
  const app = buildServer({ config, pool, logger, sessions, startedAt })

  try {
    await app.listen({ port: config.PORT, host: '0.0.0.0' })
  } catch (err) {
    logger.fatal({ err }, 'failed to bind http server')
    await pool.end().catch(() => {})
    process.exit(1)
  }

  void sessions.restoreAll().catch((err) => {
    logger.error({ err }, 'session restore failed')
  })

  let shuttingDown = false
  const shutdown = async (signal: string): Promise<void> => {
    if (shuttingDown) return
    shuttingDown = true
    logger.info({ signal }, 'shutting down')
    try {
      await app.close()
      await sessions.shutdown()
      await pool.end()
      logger.info('shutdown complete')
      process.exit(0)
    } catch (err) {
      logger.error({ err }, 'error during shutdown')
      process.exit(1)
    }
  }

  process.on('SIGTERM', () => void shutdown('SIGTERM'))
  process.on('SIGINT', () => void shutdown('SIGINT'))
}

void main()
