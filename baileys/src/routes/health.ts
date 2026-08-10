import type { AppInstance } from '../app.js'
import { pingDb, type Pool } from '../db.js'

export interface HealthBody {
  status: 'ok' | 'degraded'
  db: 'ok' | 'down'
  uptime: number
}

export function registerHealthRoutes(app: AppInstance, pool: Pool, startedAt: number): void {
  app.get('/health', async (_req, reply) => {
    const dbUp = await pingDb(pool)
    const body: HealthBody = {
      status: dbUp ? 'ok' : 'degraded',
      db: dbUp ? 'ok' : 'down',
      uptime: Math.round((Date.now() - startedAt) / 1000),
    }
    return reply.code(dbUp ? 200 : 503).send(body)
  })
}
