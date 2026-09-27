import { timingSafeEqual } from 'node:crypto'
import type { FastifyRequest } from 'fastify'
import type { AppInstance } from './app.js'
import { unauthorized } from './errors.js'

const PUBLIC_PATHS = new Set(['/health'])

function tokensMatch(a: string, b: string): boolean {
  const bufA = Buffer.from(a, 'utf8')
  const bufB = Buffer.from(b, 'utf8')
  if (bufA.length !== bufB.length) return false
  return timingSafeEqual(bufA, bufB)
}

function extractBearer(req: FastifyRequest): string | null {
  const header = req.headers.authorization
  if (!header) return null
  const [scheme, ...rest] = header.split(' ')
  if (scheme?.toLowerCase() !== 'bearer') return null
  const token = rest.join(' ').trim()
  return token.length > 0 ? token : null
}

/** Every route except `/health` requires `Authorization: Bearer <API_KEY>`. */
export function registerAuth(app: AppInstance, apiKey: string): void {
  app.addHook('onRequest', async (req) => {
    const path = req.url.split('?')[0] ?? req.url
    if (PUBLIC_PATHS.has(path)) return

    const token = extractBearer(req)
    if (token === null || !tokensMatch(token, apiKey)) {
      req.log.warn({ path }, 'rejected unauthenticated request')
      throw unauthorized()
    }
  })
}
