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
  owner_id: string | null
  created_at: Date
  updated_at: Date
}

export async function createSession(pool: Pool, label: string, ownerId?: string): Promise<SessionRow> {
  const { rows } = await pool.query<SessionRow>(
    'INSERT INTO wa_sessions (id, label, owner_id) VALUES ($1, $2, $3) RETURNING *',
    [randomUUID(), label, ownerId ?? null],
  )
  return rows[0]!
}

// listSessionsByOwner and getOwnedSession are the owner-scoped variants used by wa-console's
// HTTP routes (see ../routes/sessions.ts). Plain getSession/listSessions below stay
// unrestricted — they're also used internally by SessionManager and metrics, which operate on
// session ids the route layer already resolved, not on a caller's identity.
export async function listSessionsByOwner(pool: Pool, ownerId: string): Promise<SessionRow[]> {
  const { rows } = await pool.query<SessionRow>(
    'SELECT * FROM wa_sessions WHERE owner_id = $1 ORDER BY created_at',
    [ownerId],
  )
  return rows
}

export async function getOwnedSession(
  pool: Pool,
  id: string,
  ownerId: string,
): Promise<SessionRow | undefined> {
  const { rows } = await pool.query<SessionRow>(
    'SELECT * FROM wa_sessions WHERE id = $1 AND owner_id = $2',
    [id, ownerId],
  )
  return rows[0]
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

export async function deleteSession(pool: Pool, id: string): Promise<void> {
  await pool.query('DELETE FROM wa_sessions WHERE id = $1', [id])
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
