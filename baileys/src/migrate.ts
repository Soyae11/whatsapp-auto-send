import { readdir, readFile } from 'node:fs/promises'
import { join } from 'node:path'
import type { Pool } from './db.js'
import type { Logger } from './logger.js'

const ADVISORY_LOCK_KEY = 8_472_119_003

export interface MigrationResult {
  applied: string[]
  alreadyApplied: string[]
}

async function listMigrationFiles(dir: string): Promise<string[]> {
  let entries: string[]
  try {
    entries = await readdir(dir)
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === 'ENOENT') return []
    throw err
  }
  return entries.filter((name) => name.endsWith('.sql')).sort()
}

export async function runMigrations(
  pool: Pool,
  dir: string,
  logger: Logger,
): Promise<MigrationResult> {
  const files = await listMigrationFiles(dir)
  const result: MigrationResult = { applied: [], alreadyApplied: [] }

  const client = await pool.connect()
  try {
    await client.query(`
      CREATE TABLE IF NOT EXISTS schema_migrations (
        version     TEXT PRIMARY KEY,
        applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
      )
    `)
    await client.query('SELECT pg_advisory_lock($1)', [ADVISORY_LOCK_KEY])

    try {
      const { rows } = await client.query<{ version: string }>(
        'SELECT version FROM schema_migrations',
      )
      const done = new Set(rows.map((r) => r.version))

      for (const file of files) {
        if (done.has(file)) {
          result.alreadyApplied.push(file)
          continue
        }

        const sql = await readFile(join(dir, file), 'utf8')
        logger.info({ migration: file }, 'applying migration')

        await client.query('BEGIN')
        try {
          await client.query(sql)
          await client.query('INSERT INTO schema_migrations (version) VALUES ($1)', [file])
          await client.query('COMMIT')
        } catch (err) {
          await client.query('ROLLBACK')
          throw new Error(`migration ${file} failed: ${(err as Error).message}`, { cause: err })
        }
        result.applied.push(file)
      }
    } finally {
      await client.query('SELECT pg_advisory_unlock($1)', [ADVISORY_LOCK_KEY])
    }
  } finally {
    client.release()
  }

  logger.info(
    { applied: result.applied.length, alreadyApplied: result.alreadyApplied.length },
    'migrations up to date',
  )
  return result
}
