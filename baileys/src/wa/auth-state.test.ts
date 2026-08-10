import { initAuthCreds } from 'baileys'
import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { createPool, type Pool } from '../db.js'
import { createLogger } from '../logger.js'
import { ensureSession } from '../sessions/repository.js'
import {
  clearAuthState,
  isFullyPaired,
  isPairingIncomplete,
  loadCreds,
  makePostgresSignalKeyStore,
  saveCreds,
  usePostgresAuthState,
} from './auth-state.js'

describe('isFullyPaired', () => {
  const complete = () => {
    const creds = initAuthCreds()
    creds.me = { id: '6287713848500:6@s.whatsapp.net', name: 'Test', lid: '139634386436280:6@lid' }
    creds.account = { details: Buffer.from([1]), accountSignature: Buffer.from([2]) }
    creds.signalIdentities = [
      { identifier: { name: '6287713848500@s.whatsapp.net', deviceId: 0 }, identifierKey: Buffer.from([3]) },
    ]
    creds.platform = 'android'
    return creds
  }

  it('accepts creds that finished pair-success', () => {
    expect(isFullyPaired(complete())).toBe(true)
  })

  it('accepts a QR pairing, which never sets registered', () => {
    const creds = complete()
    expect(creds.registered).toBe(false)
    expect(isFullyPaired(creds)).toBe(true)
  })

  it('ignores registered=true when the account is missing', () => {
    const creds = initAuthCreds()
    creds.registered = true
    creds.me = { id: '6287713848500@s.whatsapp.net', name: '~' }
    expect(isFullyPaired(creds)).toBe(false)
    expect(isPairingIncomplete(creds)).toBe(true)
  })

  it('rejects fresh creds, which are unpaired rather than incomplete', () => {
    const creds = initAuthCreds()
    expect(isFullyPaired(creds)).toBe(false)
    expect(isPairingIncomplete(creds)).toBe(false)
  })

  it('rejects each individually missing piece', () => {
    for (const drop of ['me', 'account', 'signalIdentities'] as const) {
      const creds = complete()
      delete creds[drop]
      expect(isFullyPaired(creds), drop).toBe(false)
    }
  })

  it('rejects an empty signalIdentities array', () => {
    const creds = complete()
    creds.signalIdentities = []
    expect(isFullyPaired(creds)).toBe(false)
  })
})

const url = process.env.DATABASE_URL
const suite = url ? describe : describe.skip

suite('postgres auth state', () => {
  const logger = createLogger('silent')
  let pool: Pool
  let sessionId: string
  let counter = 0

  beforeAll(async () => {
    pool = createPool(url!, logger)
  })

  beforeEach(async () => {
    sessionId = `test-${process.pid}-${++counter}`
    await ensureSession(pool, sessionId, `test session ${counter}`)
  })

  afterAll(async () => {
    await pool.query("DELETE FROM wa_sessions WHERE id LIKE 'test-%'")
    await pool.end()
  })

  describe('creds', () => {
    it('returns fresh creds for a session that has never paired', async () => {
      const creds = await loadCreds(pool, sessionId)
      expect(creds.registered).toBe(false)
      expect(creds.registrationId).toBeTypeOf('number')
      expect(creds.noiseKey.private).toBeDefined()

      const { rows } = await pool.query('SELECT 1 FROM wa_auth_creds WHERE session_id = $1', [
        sessionId,
      ])
      expect(rows).toHaveLength(0)
    })

    it('survives a restart: creds reload byte-identically from a cold read', async () => {
      const creds = initAuthCreds()
      creds.registered = true
      creds.me = { id: '62812000000:1@s.whatsapp.net', name: 'Test' }
      creds.routingInfo = Buffer.from([0, 127, 255])
      await saveCreds(pool, sessionId, creds)

      const coldPool = createPool(url!, logger)
      try {
        const restored = await loadCreds(coldPool, sessionId)

        expect(restored.registered).toBe(true)
        expect(restored.me?.id).toBe(creds.me.id)
        expect(restored.registrationId).toBe(creds.registrationId)
        expect(restored.advSecretKey).toBe(creds.advSecretKey)
        expect(Buffer.from(restored.noiseKey.private).equals(Buffer.from(creds.noiseKey.private)))
          .toBe(true)
        expect(
          Buffer.from(restored.signedIdentityKey.private).equals(
            Buffer.from(creds.signedIdentityKey.private),
          ),
        ).toBe(true)
        expect(
          Buffer.from(restored.signedPreKey.signature).equals(
            Buffer.from(creds.signedPreKey.signature),
          ),
        ).toBe(true)
        expect(Buffer.from(restored.routingInfo!).equals(creds.routingInfo)).toBe(true)
      } finally {
        await coldPool.end()
      }
    })

    it('stores binary as a BufferJSON marker, not a numeric-key blob', async () => {
      await saveCreds(pool, sessionId, initAuthCreds())
      const { rows } = await pool.query<{ creds: string }>(
        'SELECT creds::text AS creds FROM wa_auth_creds WHERE session_id = $1',
        [sessionId],
      )
      expect(rows[0]!.creds).toContain('"type": "Buffer"')
      expect(rows[0]!.creds).not.toMatch(/"noiseKey":\s*\{\s*"private":\s*\{\s*"0":/)
    })

    it('overwrites on repeated saves rather than erroring', async () => {
      const creds = initAuthCreds()
      await saveCreds(pool, sessionId, creds)
      creds.nextPreKeyId = 99
      await saveCreds(pool, sessionId, creds)

      expect((await loadCreds(pool, sessionId)).nextPreKeyId).toBe(99)
      const { rows } = await pool.query('SELECT 1 FROM wa_auth_creds WHERE session_id = $1', [
        sessionId,
      ])
      expect(rows).toHaveLength(1)
    })
  })

  describe('key store', () => {
    it('round-trips a pre-key with its binary intact', async () => {
      const store = makePostgresSignalKeyStore(pool, sessionId)
      const keyPair = { public: Buffer.from([1, 2, 3]), private: Buffer.from([4, 5, 6]) }
      await store.set({ 'pre-key': { '1': keyPair } })

      const got = await store.get('pre-key', ['1'])
      expect(Buffer.from(got['1']!.public).equals(keyPair.public)).toBe(true)
      expect(Buffer.from(got['1']!.private).equals(keyPair.private)).toBe(true)
    })

    it('omits missing ids instead of returning null entries', async () => {
      const store = makePostgresSignalKeyStore(pool, sessionId)
      await store.set({ session: { present: Buffer.from([1]) } })

      const got = await store.get('session', ['present', 'missing'])
      expect(Object.keys(got)).toEqual(['present'])
      expect('missing' in got).toBe(false)
    })

    it('returns an empty map for no ids without hitting the database', async () => {
      const store = makePostgresSignalKeyStore(pool, sessionId)
      expect(await store.get('session', [])).toEqual({})
    })

    it('treats a null value as a delete, not a stored null', async () => {
      const store = makePostgresSignalKeyStore(pool, sessionId)
      await store.set({ session: { a: Buffer.from([1]), b: Buffer.from([2]) } })
      await store.set({ session: { a: null } })

      const got = await store.get('session', ['a', 'b'])
      expect(Object.keys(got)).toEqual(['b'])

      const { rows } = await pool.query(
        'SELECT 1 FROM wa_auth_keys WHERE session_id = $1 AND key_id = $2',
        [sessionId, 'a'],
      )
      expect(rows).toHaveLength(0)
    })

    it('applies upserts and deletes from one call in a single transaction', async () => {
      const store = makePostgresSignalKeyStore(pool, sessionId)
      await store.set({ 'pre-key': { old: { public: Buffer.from([1]), private: Buffer.from([2]) } } })

      await store.set({
        'pre-key': {
          old: null,
          new: { public: Buffer.from([3]), private: Buffer.from([4]) },
        },
        session: { s1: Buffer.from([5]) },
      })

      expect(Object.keys(await store.get('pre-key', ['old', 'new']))).toEqual(['new'])
      expect(Object.keys(await store.get('session', ['s1']))).toEqual(['s1'])
    })

    it('overwrites an existing key rather than conflicting', async () => {
      const store = makePostgresSignalKeyStore(pool, sessionId)
      await store.set({ session: { s: Buffer.from([1]) } })
      await store.set({ session: { s: Buffer.from([2, 2]) } })

      const got = await store.get('session', ['s'])
      expect(Buffer.from(got['s']!).equals(Buffer.from([2, 2]))).toBe(true)
    })

    it('stores non-binary value types too', async () => {
      const store = makePostgresSignalKeyStore(pool, sessionId)
      await store.set({
        'lid-mapping': { m: '62812000000@lid' },
        'device-list': { d: ['0', '1'] },
        'sender-key-memory': { k: { '62812000000@s.whatsapp.net': true } },
      })

      expect((await store.get('lid-mapping', ['m']))['m']).toBe('62812000000@lid')
      expect((await store.get('device-list', ['d']))['d']).toEqual(['0', '1'])
      expect((await store.get('sender-key-memory', ['k']))['k']).toEqual({
        '62812000000@s.whatsapp.net': true,
      })
    })

    it('keeps key types in separate namespaces', async () => {
      const store = makePostgresSignalKeyStore(pool, sessionId)
      await store.set({ session: { same: Buffer.from([1]) } })
      await store.set({ 'sender-key': { same: Buffer.from([2]) } })

      expect(Buffer.from((await store.get('session', ['same']))['same']!)).toEqual(
        Buffer.from([1]),
      )
      expect(Buffer.from((await store.get('sender-key', ['same']))['same']!)).toEqual(
        Buffer.from([2]),
      )
    })

    it('isolates sessions that use the same key ids', async () => {
      const otherId = `${sessionId}-other`
      await ensureSession(pool, otherId, 'other')

      const mine = makePostgresSignalKeyStore(pool, sessionId)
      const theirs = makePostgresSignalKeyStore(pool, otherId)
      await mine.set({ session: { shared: Buffer.from([1]) } })
      await theirs.set({ session: { shared: Buffer.from([2]) } })

      expect(Buffer.from((await mine.get('session', ['shared']))['shared']!)).toEqual(
        Buffer.from([1]),
      )
      expect(Buffer.from((await theirs.get('session', ['shared']))['shared']!)).toEqual(
        Buffer.from([2]),
      )
    })

    it('does nothing for an empty set', async () => {
      const store = makePostgresSignalKeyStore(pool, sessionId)
      await expect(store.set({})).resolves.toBeUndefined()
      await expect(store.set({ session: {} })).resolves.toBeUndefined()
    })

    it('clear() drops every key for the session and nobody else', async () => {
      const otherId = `${sessionId}-other`
      await ensureSession(pool, otherId, 'other')
      const mine = makePostgresSignalKeyStore(pool, sessionId)
      const theirs = makePostgresSignalKeyStore(pool, otherId)
      await mine.set({ session: { a: Buffer.from([1]) } })
      await theirs.set({ session: { a: Buffer.from([2]) } })

      await mine.clear!()

      expect(await mine.get('session', ['a'])).toEqual({})
      expect(Object.keys(await theirs.get('session', ['a']))).toEqual(['a'])
    })
  })

  describe('usePostgresAuthState', () => {
    it('hands back a cached key store that reads and writes through to Postgres', async () => {
      const { state, saveCreds: persist } = await usePostgresAuthState(pool, sessionId, logger)

      state.creds.registered = true
      await persist()
      await state.keys.set({ session: { s: Buffer.from([7, 7]) } })

      // A second call is what a restart produces: cold creds, cold cache.
      const reopened = await usePostgresAuthState(pool, sessionId, logger)
      expect(reopened.state.creds.registered).toBe(true)
      expect(
        Buffer.from((await reopened.state.keys.get('session', ['s']))['s']!).equals(
          Buffer.from([7, 7]),
        ),
      ).toBe(true)
    })

    it('persists creds mutated in place after the state was created', async () => {
      const { state, saveCreds: persist } = await usePostgresAuthState(pool, sessionId, logger)
      state.creds.nextPreKeyId = 123
      state.creds.advSecretKey = 'mutated-secret'
      await persist()

      const restored = await loadCreds(pool, sessionId)
      expect(restored.nextPreKeyId).toBe(123)
      expect(restored.advSecretKey).toBe('mutated-secret')
    })
  })

  describe('clearAuthState', () => {
    it('removes creds and keys but keeps the session row', async () => {
      const store = makePostgresSignalKeyStore(pool, sessionId)
      await saveCreds(pool, sessionId, initAuthCreds())
      await store.set({ session: { a: Buffer.from([1]) } })

      await clearAuthState(pool, sessionId)

      expect(await store.get('session', ['a'])).toEqual({})
      const creds = await pool.query('SELECT 1 FROM wa_auth_creds WHERE session_id = $1', [
        sessionId,
      ])
      expect(creds.rows).toHaveLength(0)
      const session = await pool.query('SELECT 1 FROM wa_sessions WHERE id = $1', [sessionId])
      expect(session.rows).toHaveLength(1)
    })

    it('leaves a session with no auth rows untouched', async () => {
      await expect(clearAuthState(pool, sessionId)).resolves.toBeUndefined()
    })
  })

  describe('schema', () => {
    it('cascades auth rows when the session is deleted', async () => {
      await saveCreds(pool, sessionId, initAuthCreds())
      await makePostgresSignalKeyStore(pool, sessionId).set({ session: { a: Buffer.from([1]) } })

      await pool.query('DELETE FROM wa_sessions WHERE id = $1', [sessionId])

      const creds = await pool.query('SELECT 1 FROM wa_auth_creds WHERE session_id = $1', [
        sessionId,
      ])
      const keys = await pool.query('SELECT 1 FROM wa_auth_keys WHERE session_id = $1', [sessionId])
      expect(creds.rows).toHaveLength(0)
      expect(keys.rows).toHaveLength(0)
    })

    it('rejects a status outside the documented set', async () => {
      await expect(
        pool.query('UPDATE wa_sessions SET status = $2 WHERE id = $1', [sessionId, 'bogus']),
      ).rejects.toThrow(/wa_sessions_status_check/)
    })
  })
})
