import type { Pool } from '../db.js'

export type SendStatus = 'pending' | 'sent' | 'failed'

export interface SentMessageRow {
  id: string
  session_id: string
  idempotency_key: string
  to_jid: string
  payload: unknown
  status: SendStatus
  wa_message_id: string | null
  error_code: string | null
  error_detail: string | null
  created_at: Date
}

const STALE_CLAIM_MS = 120_000

export type ClaimResult =
  | { outcome: 'claimed'; row: SentMessageRow }
  | { outcome: 'duplicate'; row: SentMessageRow }
  | { outcome: 'in_flight'; row: SentMessageRow }

export async function claimSend(
  pool: Pool,
  input: {
    sessionId: string
    idempotencyKey: string
    toJid: string
    payload: unknown
  },
): Promise<ClaimResult> {
  const { rows } = await pool.query<SentMessageRow>(
    `INSERT INTO wa_sent_messages (session_id, idempotency_key, to_jid, payload, status)
     VALUES ($1, $2, $3, $4::jsonb, 'pending')
     ON CONFLICT (idempotency_key) DO NOTHING
     RETURNING *`,
    [input.sessionId, input.idempotencyKey, input.toJid, JSON.stringify(input.payload)],
  )

  const claimed = rows[0]
  if (claimed) return { outcome: 'claimed', row: claimed }

  const existing = await findByIdempotencyKey(pool, input.idempotencyKey)
  // The conflicting row cannot vanish: nothing deletes from this table.
  if (!existing) throw new Error(`idempotency key ${input.idempotencyKey} vanished mid-claim`)

  if (existing.status === 'sent') return { outcome: 'duplicate', row: existing }

  if (existing.status === 'pending') {
    const age = Date.now() - existing.created_at.getTime()
    if (age < STALE_CLAIM_MS) return { outcome: 'in_flight', row: existing }
  }

  const { rows: retaken } = await pool.query<SentMessageRow>(
    `UPDATE wa_sent_messages
     SET status = 'pending', to_jid = $2, payload = $3::jsonb, created_at = now(),
         wa_message_id = NULL, error_code = NULL, error_detail = NULL
     WHERE idempotency_key = $1
     RETURNING *`,
    [input.idempotencyKey, input.toJid, JSON.stringify(input.payload)],
  )
  return { outcome: 'claimed', row: retaken[0]! }
}

export async function findByIdempotencyKey(
  pool: Pool,
  key: string,
): Promise<SentMessageRow | undefined> {
  const { rows } = await pool.query<SentMessageRow>(
    'SELECT * FROM wa_sent_messages WHERE idempotency_key = $1',
    [key],
  )
  return rows[0]
}

export async function markSent(
  pool: Pool,
  key: string,
  waMessageId: string | null,
  toJid: string,
): Promise<void> {
  await pool.query(
    `UPDATE wa_sent_messages SET status = 'sent', wa_message_id = $2, to_jid = $3,
            error_code = NULL, error_detail = NULL
     WHERE idempotency_key = $1`,
    [key, waMessageId, toJid],
  )
}

export async function markFailed(
  pool: Pool,
  key: string,
  errorCode: string,
  errorDetail: string | null,
): Promise<void> {
  await pool.query(
    `UPDATE wa_sent_messages SET status = 'failed', error_code = $2, error_detail = $3
     WHERE idempotency_key = $1`,
    [key, errorCode, errorDetail],
  )
}
