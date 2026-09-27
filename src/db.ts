import pg from 'pg'
import type { Logger } from './logger.js'

export type Pool = pg.Pool

export function createPool(databaseUrl: string, logger: Logger): Pool {
  const pool = new pg.Pool({
    connectionString: databaseUrl,
    max: 10,
    connectionTimeoutMillis: 3000,
    idleTimeoutMillis: 30_000,
  })

  pool.on('error', (err) => {
    logger.warn({ err }, 'idle postgres client error')
  })

  return pool
}

export async function pingDb(pool: Pool): Promise<boolean> {
  try {
    await pool.query('SELECT 1')
    return true
  } catch {
    return false
  }
}
