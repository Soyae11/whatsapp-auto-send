import { randomUUID } from 'node:crypto'
import type { Pool } from '../db.js'

export type SessionStatus =
  | 'new'
  | 'pairing'
  | 'connected'
  | 'disconnected'
  | 'logged_out'
  | 'unhealthy'

export interface SessionRow {
  id: string
  label: string
  status: SessionStatus
  phone_number: string | null
  created_at: Date
  updated_at: Date
}

export async function createSession(pool: Pool, label: string): Promise<SessionRow> {
  const { rows } = await pool.query<SessionRow>(
    'INSERT INTO wa_sessions (id, label) VALUES ($1, $2) RETURNING *',
    [randomUUID(), label],
  )
  return rows[0]!
}

export async function ensureSession(pool: Pool, id: string, label: string): Promise<void> {
  await pool.query(
    `INSERT INTO wa_sessions (id, label) VALUES ($1, $2)
     ON CONFLICT (id) DO NOTHING`,
    [id, label],
  )
}

export async function getSession(pool: Pool, id: string): Promise<SessionRow | undefined> {
  const { rows } = await pool.query<SessionRow>('SELECT * FROM wa_sessions WHERE id = $1', [id])
  return rows[0]
}

export async function listSessions(pool: Pool): Promise<SessionRow[]> {
  const { rows } = await pool.query<SessionRow>('SELECT * FROM wa_sessions ORDER BY created_at')
  return rows
}

export async function listRestorableSessions(pool: Pool): Promise<SessionRow[]> {
  const { rows } = await pool.query<SessionRow>(
    `SELECT * FROM wa_sessions
     WHERE status IN ('connected', 'disconnected', 'unhealthy')
     ORDER BY updated_at DESC`,
  )
  return rows
}

export async function setSessionStatus(
  pool: Pool,
  id: string,
  status: SessionStatus,
): Promise<void> {
  await pool.query('UPDATE wa_sessions SET status = $2, updated_at = now() WHERE id = $1', [
    id,
    status,
  ])
}

export async function setSessionConnected(
  pool: Pool,
  id: string,
  phoneNumber: string,
): Promise<void> {
  await pool.query(
    `UPDATE wa_sessions SET status = 'connected', phone_number = $2, updated_at = now()
     WHERE id = $1`,
    [id, phoneNumber],
  )
}
