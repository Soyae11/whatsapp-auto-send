import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { createPool, type Pool } from './db.js'
import { createLogger } from './logger.js'
import { runMigrations } from './migrate.js'

const url = process.env.DATABASE_URL
const suite = url ? describe : describe.skip

suite('runMigrations', () => {
  const logger = createLogger('silent')
  let pool: Pool
  let dir: string

  beforeAll(async () => {
    pool = createPool(url!, logger)
    dir = await mkdtemp(join(tmpdir(), 'baileys-migrations-'))
    await writeFile(join(dir, '9002_migtest_add_column.sql'), 'ALTER TABLE mig_test ADD COLUMN note TEXT;')
    await writeFile(join(dir, '9001_migtest_create.sql'), 'CREATE TABLE mig_test (id INT PRIMARY KEY);')
    await writeFile(join(dir, 'notes.txt'), 'not a migration')
  })

  afterAll(async () => {
    await pool.query('DROP TABLE IF EXISTS mig_test')
    await pool.query("DELETE FROM schema_migrations WHERE version LIKE '9%_migtest_%'")
    await pool.end()
    await rm(dir, { recursive: true, force: true })
  })

  it('applies .sql files in filename order and records them', async () => {
    const result = await runMigrations(pool, dir, logger)
    expect(result.applied).toEqual(['9001_migtest_create.sql', '9002_migtest_add_column.sql'])

    const { rows } = await pool.query<{ column_name: string }>(
      "SELECT column_name FROM information_schema.columns WHERE table_name = 'mig_test' ORDER BY column_name",
    )
    expect(rows.map((r) => r.column_name)).toEqual(['id', 'note'])
  })

  it('is idempotent on a second run', async () => {
    const result = await runMigrations(pool, dir, logger)
    expect(result.applied).toEqual([])
    expect(result.alreadyApplied).toEqual(['9001_migtest_create.sql', '9002_migtest_add_column.sql'])
  })

  it('rolls back a failing migration and leaves it unrecorded', async () => {
    const badDir = await mkdtemp(join(tmpdir(), 'baileys-bad-'))
    await writeFile(
      join(badDir, '9009_migtest_bad.sql'),
      'CREATE TABLE mig_bad (id INT); INVALID SQL HERE;',
    )
    await expect(runMigrations(pool, badDir, logger)).rejects.toThrow(/9009_migtest_bad\.sql/)

    const { rows } = await pool.query(
      "SELECT to_regclass('mig_bad') AS tbl, (SELECT count(*) FROM schema_migrations WHERE version = '9009_migtest_bad.sql') AS n",
    )
    expect(rows[0]).toMatchObject({ tbl: null, n: '0' })
    await rm(badDir, { recursive: true, force: true })
  })

  it('treats a missing migrations directory as no work', async () => {
    const result = await runMigrations(pool, join(dir, 'does-not-exist'), logger)
    expect(result.applied).toEqual([])
  })
})
