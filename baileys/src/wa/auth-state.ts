import {
  initAuthCreds,
  makeCacheableSignalKeyStore,
  type AuthenticationCreds,
  type AuthenticationState,
  type SignalDataSet,
  type SignalDataTypeMap,
  type SignalKeyStore,
} from 'baileys'
import type { ILogger } from 'baileys/lib/Utils/logger.js'
import type { Pool } from '../db.js'
import { deserialise, serialise } from './serialisation.js'

export interface PostgresAuthState {
  state: AuthenticationState
  saveCreds: () => Promise<void>
}

export function isFullyPaired(creds: AuthenticationCreds): boolean {
  return Boolean(creds.me?.id && creds.account && creds.signalIdentities?.length)
}

export function isPairingIncomplete(creds: AuthenticationCreds): boolean {
  return Boolean(creds.me?.id) && !isFullyPaired(creds)
}

export async function loadCreds(pool: Pool, sessionId: string): Promise<AuthenticationCreds> {
  const { rows } = await pool.query<{ creds: string }>(
    'SELECT creds::text AS creds FROM wa_auth_creds WHERE session_id = $1',
    [sessionId],
  )
  const row = rows[0]
  return row ? deserialise<AuthenticationCreds>(row.creds) : initAuthCreds()
}

export async function saveCreds(
  pool: Pool,
  sessionId: string,
  creds: AuthenticationCreds,
): Promise<void> {
  await pool.query(
    `INSERT INTO wa_auth_creds (session_id, creds, updated_at)
     VALUES ($1, $2::jsonb, now())
     ON CONFLICT (session_id)
     DO UPDATE SET creds = EXCLUDED.creds, updated_at = now()`,
    [sessionId, serialise(creds)],
  )
}

export function makePostgresSignalKeyStore(pool: Pool, sessionId: string): SignalKeyStore {
  return {
    async get<T extends keyof SignalDataTypeMap>(
      type: T,
      ids: string[],
    ): Promise<{ [id: string]: SignalDataTypeMap[T] }> {
      const found: { [id: string]: SignalDataTypeMap[T] } = {}
      if (ids.length === 0) return found

      const { rows } = await pool.query<{ key_id: string; key_data: string }>(
        `SELECT key_id, key_data::text AS key_data
         FROM wa_auth_keys
         WHERE session_id = $1 AND key_type = $2 AND key_id = ANY($3::text[])`,
        [sessionId, type, ids],
      )
      for (const row of rows) {
        found[row.key_id] = deserialise<SignalDataTypeMap[T]>(row.key_data)
      }
      return found
    },

    async set(data: SignalDataSet): Promise<void> {
      const upsertTypes: string[] = []
      const upsertIds: string[] = []
      const upsertData: string[] = []
      const deleteTypes: string[] = []
      const deleteIds: string[] = []

      for (const [type, entries] of Object.entries(data)) {
        if (!entries) continue
        for (const [id, value] of Object.entries(entries)) {
          if (value === null || value === undefined) {
            deleteTypes.push(type)
            deleteIds.push(id)
          } else {
            upsertTypes.push(type)
            upsertIds.push(id)
            upsertData.push(serialise(value))
          }
        }
      }

      if (upsertTypes.length === 0 && deleteTypes.length === 0) return

      const client = await pool.connect()
      try {
        await client.query('BEGIN')

        if (upsertTypes.length > 0) {
          await client.query(
            `INSERT INTO wa_auth_keys (session_id, key_type, key_id, key_data)
             SELECT $1, t.key_type, t.key_id, t.key_data::jsonb
             FROM unnest($2::text[], $3::text[], $4::text[]) AS t(key_type, key_id, key_data)
             ON CONFLICT (session_id, key_type, key_id)
             DO UPDATE SET key_data = EXCLUDED.key_data`,
            [sessionId, upsertTypes, upsertIds, upsertData],
          )
        }

        if (deleteTypes.length > 0) {
          await client.query(
            `DELETE FROM wa_auth_keys
             WHERE session_id = $1
               AND (key_type, key_id) IN (SELECT * FROM unnest($2::text[], $3::text[]))`,
            [sessionId, deleteTypes, deleteIds],
          )
        }

        await client.query('COMMIT')
      } catch (err) {
        await client.query('ROLLBACK').catch(() => {})
        throw err
      } finally {
        client.release()
      }
    },

    async clear(): Promise<void> {
      await pool.query('DELETE FROM wa_auth_keys WHERE session_id = $1', [sessionId])
    },
  }
}

export async function usePostgresAuthState(
  pool: Pool,
  sessionId: string,
  logger?: ILogger,
): Promise<PostgresAuthState> {
  const creds = await loadCreds(pool, sessionId)
  const store = makePostgresSignalKeyStore(pool, sessionId)

  return {
    state: {
      creds,
      keys: makeCacheableSignalKeyStore(store, logger),
    },
    saveCreds: () => saveCreds(pool, sessionId, creds),
  }
}

export async function clearAuthState(pool: Pool, sessionId: string): Promise<void> {
  const client = await pool.connect()
  try {
    await client.query('BEGIN')
    await client.query('DELETE FROM wa_auth_keys WHERE session_id = $1', [sessionId])
    await client.query('DELETE FROM wa_auth_creds WHERE session_id = $1', [sessionId])
    await client.query('COMMIT')
  } catch (err) {
    await client.query('ROLLBACK').catch(() => {})
    throw err
  } finally {
    client.release()
  }
}
