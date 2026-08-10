import { loadMigrationConfig } from './config.js'
import { createPool } from './db.js'
import { createLogger } from './logger.js'
import { runMigrations } from './migrate.js'
import { MIGRATIONS_DIR } from './paths.js'

const config = loadMigrationConfig()
const logger = createLogger(config.LOG_LEVEL)
const pool = createPool(config.DATABASE_URL, logger)

try {
  await runMigrations(pool, MIGRATIONS_DIR, logger)
} catch (err) {
  logger.fatal({ err }, 'migrations failed')
  process.exitCode = 1
} finally {
  await pool.end()
}
