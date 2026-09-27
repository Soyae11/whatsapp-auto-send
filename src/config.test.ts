import { describe, expect, it } from 'vitest'
import { loadConfig, loadMigrationConfig } from './config.js'

const valid = {
  DATABASE_URL: 'postgres://user:pw@localhost:5432/baileys',
  PORT: '3000',
  LOG_LEVEL: 'info',
  API_KEY: 'a'.repeat(32),
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
    expect(message).toContain('API_KEY')
  })

  it('rejects a short api key', () => {
    expect(() => loadConfig({ ...valid, API_KEY: 'short' })).toThrow(/API_KEY/)
  })

  it('treats blank variables as unset', () => {
    const config = loadConfig({ ...valid, WEBHOOK_URL: '', WEBHOOK_SECRET: '', WEBHOOK_TIMEOUT_MS: '' })
    expect(config.WEBHOOK_URL).toBeUndefined()
    expect(config.WEBHOOK_TIMEOUT_MS).toBe(5_000)
  })

  it('still requires a webhook secret when a webhook url is set', () => {
    expect(() => loadConfig({ ...valid, WEBHOOK_URL: 'http://localhost/hook' })).toThrow(/WEBHOOK_SECRET/)
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
