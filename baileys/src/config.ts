import { z } from 'zod'

const databaseUrl = z.string().min(1, 'must be a Postgres connection string')

const logLevel = z.enum(['fatal', 'error', 'warn', 'info', 'debug', 'trace', 'silent'])

const port = z
  .string()
  .regex(/^\d+$/, 'must be a port number')
  .transform(Number)
  .pipe(z.number().int().positive().max(65535, 'must be at most 65535'))

const schema = z
  .object({
    DATABASE_URL: databaseUrl,
    PORT: port,
    LOG_LEVEL: logLevel,
    // Two credentials, not one. The dispatcher may send and read; the console may read and
    // manage sessions. Leaving CONSOLE_API_KEY unset revokes console access without
    // touching the send path.
    DISPATCHER_API_KEY: z.string().min(16, 'must be at least 16 characters'),
    CONSOLE_API_KEY: z.string().min(16, 'must be at least 16 characters').optional(),

    WEBHOOK_URL: z.url('must be an http(s) URL').optional(),
    WEBHOOK_SECRET: z.string().min(16, 'must be at least 16 characters').optional(),
    WEBHOOK_TIMEOUT_MS: z
      .string()
      .regex(/^\d+$/, 'must be a number of milliseconds')
      .transform(Number)
      .pipe(z.number().int().min(100).max(30_000))
      .default(5_000),
  })
  .refine((cfg) => !cfg.WEBHOOK_URL || Boolean(cfg.WEBHOOK_SECRET), {
    path: ['WEBHOOK_SECRET'],
    message: 'is required when WEBHOOK_URL is set',
  })
  .refine((cfg) => cfg.CONSOLE_API_KEY !== cfg.DISPATCHER_API_KEY, {
    path: ['CONSOLE_API_KEY'],
    message:
      'must differ from DISPATCHER_API_KEY — one shared token means neither client can be revoked without breaking the other',
  })

const migrationSchema = z.object({
  DATABASE_URL: databaseUrl,
  LOG_LEVEL: logLevel.default('info'),
})

export type Config = z.infer<typeof schema>
export type MigrationConfig = z.infer<typeof migrationSchema>

function parse<T>(schema: z.ZodType<T>, env: NodeJS.ProcessEnv): T {
  const result = schema.safeParse(env)
  if (result.success) return result.data

  const lines = result.error.issues.map((issue) => {
    const name = issue.path.join('.') || '(root)'
    return `  ${name}: ${issue.message}`
  })
  throw new Error(`Invalid environment configuration:\n${lines.join('\n')}`)
}

export function loadConfig(env: NodeJS.ProcessEnv = process.env): Config {
  return parse(schema, env)
}

export function loadMigrationConfig(env: NodeJS.ProcessEnv = process.env): MigrationConfig {
  return parse(migrationSchema, env)
}
