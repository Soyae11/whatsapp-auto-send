import { describe, expect, it } from 'vitest'
import { loadConfig, loadMigrationConfig } from './config.js'

const valid = {
  DATABASE_URL: 'postgres://user:pw@localhost:5432/baileys',
  PORT: '3000',
  LOG_LEVEL: 'info',
  DISPATCHER_API_KEY: 'a'.repeat(32),
  CONSOLE_API_KEY: 'b'.repeat(32),
}

describe('loadConfig', () => {
  it('parses a valid environment and coerces PORT to a number', () => {
    const config = loadConfig(valid)
    expect(config.PORT).toBe(3000)
    expect(config.LOG_LEVEL).toBe('info')
  })

  it('ignores unrelated environment variables', () => {
    expect(() => loadConfig({ ...valid, HOME: '/root', SHLVL: '1' })).not.toThrow()
  })

  it('reports every missing variable in one message', () => {
    let message = ''
    try {
      loadConfig({})
    } catch (err) {
      message = (err as Error).message
    }
    expect(message).toContain('DATABASE_URL')
    expect(message).toContain('PORT')
    expect(message).toContain('LOG_LEVEL')
    expect(message).toContain('DISPATCHER_API_KEY')
  })

  it('rejects a short api key', () => {
    expect(() => loadConfig({ ...valid, DISPATCHER_API_KEY: 'short' })).toThrow(/DISPATCHER_API_KEY/)
    expect(() => loadConfig({ ...valid, CONSOLE_API_KEY: 'short' })).toThrow(/CONSOLE_API_KEY/)
  })

  it('treats an absent console key as console access being switched off', () => {
    const { CONSOLE_API_KEY: _omitted, ...withoutConsole } = valid
    expect(() => loadConfig(withoutConsole)).not.toThrow()
    expect(loadConfig(withoutConsole).CONSOLE_API_KEY).toBeUndefined()
  })

  it('rejects the console and dispatcher keys being the same secret', () => {
    // Sharing one token is exactly the problem the two keys exist to remove: the gateway
    // could no longer tell the two clients apart, and revoking one would break the other.
    expect(() => loadConfig({ ...valid, CONSOLE_API_KEY: valid.DISPATCHER_API_KEY })).toThrow(
      /CONSOLE_API_KEY/,
    )
  })

  it('rejects a non-numeric or out-of-range port', () => {
    expect(() => loadConfig({ ...valid, PORT: 'http' })).toThrow(/PORT/)
    expect(() => loadConfig({ ...valid, PORT: '70000' })).toThrow(/PORT/)
  })

  it('rejects an unknown log level', () => {
    expect(() => loadConfig({ ...valid, LOG_LEVEL: 'verbose' })).toThrow(/LOG_LEVEL/)
  })
})

describe('loadMigrationConfig', () => {
  it('needs only DATABASE_URL and defaults the log level', () => {
    const config = loadMigrationConfig({ DATABASE_URL: valid.DATABASE_URL })
    expect(config.DATABASE_URL).toBe(valid.DATABASE_URL)
    expect(config.LOG_LEVEL).toBe('info')
  })

  it('still honours an explicit log level', () => {
    expect(loadMigrationConfig({ DATABASE_URL: valid.DATABASE_URL, LOG_LEVEL: 'debug' }).LOG_LEVEL)
      .toBe('debug')
  })

  it('rejects a missing DATABASE_URL', () => {
    expect(() => loadMigrationConfig({})).toThrow(/DATABASE_URL/)
  })
})
