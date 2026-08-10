import { initAuthCreds } from 'baileys'
import { describe, expect, it } from 'vitest'
import { deserialise, serialise } from './serialisation.js'

const isBinary = (v: unknown): v is Uint8Array => v instanceof Uint8Array

function normalise(value: unknown): unknown {
  if (isBinary(value)) return { __bytes: Buffer.from(value).toString('base64') }
  if (Array.isArray(value)) return value.map(normalise)
  if (value !== null && typeof value === 'object') {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>).map(([k, v]) => [k, normalise(v)]),
    )
  }
  return value
}

const roundTrip = <T>(value: T): T => deserialise<T>(serialise(value))

describe('BufferJSON round-trip', () => {
  it('restores real Baileys creds byte-identically', () => {
    const creds = initAuthCreds()
    const restored = roundTrip(creds)

    expect(normalise(restored)).toEqual(normalise(creds))

    expect(Buffer.from(restored.noiseKey.private)).toEqual(Buffer.from(creds.noiseKey.private))
    expect(Buffer.from(restored.signedIdentityKey.public)).toEqual(
      Buffer.from(creds.signedIdentityKey.public),
    )
    expect(Buffer.from(restored.signedPreKey.signature)).toEqual(
      Buffer.from(creds.signedPreKey.signature),
    )
    expect(restored.signedPreKey.keyId).toBe(creds.signedPreKey.keyId)
    expect(restored.registrationId).toBe(creds.registrationId)
    expect(restored.advSecretKey).toBe(creds.advSecretKey)
  })

  it('encodes binary as a base64 marker, not a numeric-key object', () => {
    const json = serialise({ key: Buffer.from([0, 1, 254, 255]) })
    expect(JSON.parse(json)).toEqual({ key: { type: 'Buffer', data: 'AAH+/w==' } })
    expect(json).not.toContain('"0":')
  })

  it('survives bytes that break naive encodings', () => {
    const cases = {
      allZero: Buffer.alloc(8),
      allHigh: Buffer.alloc(8, 0xff),
      nulInMiddle: Buffer.from([0x61, 0x00, 0x62]),
      empty: Buffer.alloc(0),
      long: Buffer.from(Array.from({ length: 1024 }, (_, i) => i % 256)),
    }
    const restored = roundTrip(cases)
    for (const [name, original] of Object.entries(cases)) {
      expect(Buffer.from(restored[name as keyof typeof cases]).equals(original), name).toBe(true)
    }
  })

  it('handles Uint8Array as well as Buffer', () => {
    const restored = roundTrip({ view: new Uint8Array([9, 8, 7]) })
    expect(Buffer.from(restored.view).equals(Buffer.from([9, 8, 7]))).toBe(true)
  })

  it('restores binary nested inside arrays and objects', () => {
    const value = {
      list: [Buffer.from([1]), Buffer.from([2])],
      deep: { deeper: { key: Buffer.from([3, 4]) } },
      identities: [{ identifier: { name: 'a', deviceId: 1 }, identifierKey: Buffer.from([5]) }],
    }
    expect(normalise(roundTrip(value))).toEqual(normalise(value))
  })

  it('leaves non-binary values alone', () => {
    const value = {
      str: 'hello',
      num: 42,
      float: 1.5,
      bool: false,
      nil: null,
      arr: [1, 'two', true],
      nested: { a: { b: 'c' } },
      emptyObj: {},
      emptyArr: [],
    }
    expect(roundTrip(value)).toEqual(value)
  })

  it('is stable across repeated round-trips', () => {
    const creds = initAuthCreds()
    const once = roundTrip(creds)
    const twice = roundTrip(once)
    expect(serialise(twice)).toBe(serialise(once))
  })
})
