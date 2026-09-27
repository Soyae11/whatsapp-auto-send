import { pino, type Logger } from 'pino'

export type { Logger }

export function createLogger(level: string): Logger {
  return pino({
    level,
    base: { service: 'whatsapp-gateway' },
    formatters: {
      level: (label) => ({ level: label }),
    },
    redact: {
      paths: ['req.headers.authorization', 'headers.authorization'],
      censor: '[redacted]',
    },
  })
}
