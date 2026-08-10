import { timingSafeEqual } from 'node:crypto'
import type { FastifyRequest } from 'fastify'
import type { AppInstance } from './app.js'
import type { Config } from './config.js'
import { forbidden, unauthorized } from './errors.js'

const PUBLIC_PATHS = new Set(['/health'])

/**
 * What a credential is allowed to do.
 *
 * `send` is deliberately not part of `manage`. The operator console needs the whole session
 * lifecycle — pair, restart, logout — but must never be able to put a message on the wire,
 * because a send that skips the dispatcher also skips the pacing that keeps the number from
 * being banned. One token that could do both is what this split exists to prevent.
 */
export type Capability = 'send' | 'read' | 'manage'

export interface Credential {
  /** Name for logs. Never the secret itself. */
  name: string
  key: string
  capabilities: readonly Capability[]
}

/**
 * Route pattern -> the capability it demands.
 *
 * Keyed on Fastify's *matched route pattern* rather than the request URL, so every
 * `/sessions/:id/send` is one entry and no caller can reach a stricter route by dressing a
 * session id up to look like a path. Anything absent from this table requires `manage`, so
 * a route added later fails closed rather than silently becoming world-readable.
 */
const ROUTE_CAPABILITIES = new Map<string, Capability>([
  ['POST /sessions/:id/send', 'send'],

  ['GET /sessions', 'read'],
  ['GET /sessions/:id', 'read'],
  ['GET /metrics', 'read'],

  ['POST /sessions', 'manage'],
  ['POST /sessions/:id/connect', 'manage'],
  ['POST /sessions/:id/pair', 'manage'],
  ['GET /sessions/:id/qr', 'manage'],
  ['GET /sessions/:id/qr/stream', 'manage'],
  ['POST /sessions/:id/restart', 'manage'],
  ['POST /sessions/:id/reset', 'manage'],
  ['POST /sessions/:id/logout', 'manage'],
  ['DELETE /sessions/:id', 'manage'],
])

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

/**
 * The capability this request needs, or null when no route matched at all — there is nothing
 * to authorise on the way to a 404, and answering 403 there would only make a missing route
 * look like a permissions problem. A route that *does* exist but is missing from the table
 * still demands `manage`, which is the fail-closed case that matters.
 */
function requiredCapability(req: FastifyRequest): Capability | null {
  const pattern = req.routeOptions?.url
  if (pattern === undefined) return null
  return ROUTE_CAPABILITIES.get(`${req.method} ${pattern}`) ?? 'manage'
}

/**
 * Every credential is compared, with no early return, so the time taken does not reveal
 * which key was presented or how many are configured.
 */
function identify(token: string, credentials: readonly Credential[]): Credential | null {
  let matched: Credential | null = null
  for (const credential of credentials) {
    if (tokensMatch(token, credential.key)) matched = credential
  }
  return matched
}

/**
 * The credentials the gateway accepts, derived from config.
 *
 * An unset `CONSOLE_API_KEY` is not an error — it is how console access is revoked, and it
 * leaves the dispatcher's send path untouched.
 */
export function credentialsFrom(
  config: Pick<Config, 'DISPATCHER_API_KEY' | 'CONSOLE_API_KEY'>,
): Credential[] {
  const credentials: Credential[] = [
    { name: 'dispatcher', key: config.DISPATCHER_API_KEY, capabilities: ['send', 'read'] },
  ]
  if (config.CONSOLE_API_KEY !== undefined) {
    credentials.push({ name: 'console', key: config.CONSOLE_API_KEY, capabilities: ['read', 'manage'] })
  }
  return credentials
}

export function registerAuth(app: AppInstance, credentials: readonly Credential[]): void {
  app.addHook('onRequest', async (req) => {
    const path = req.url.split('?')[0] ?? req.url
    if (PUBLIC_PATHS.has(path)) return

    const token = extractBearer(req)
    const credential = token === null ? null : identify(token, credentials)
    if (credential === null) {
      req.log.warn({ path }, 'rejected unauthenticated request')
      throw unauthorized()
    }

    const needed = requiredCapability(req)
    if (needed !== null && !credential.capabilities.includes(needed)) {
      req.log.warn({ path, client: credential.name, needed }, 'rejected unauthorised request')
      throw forbidden(`the ${credential.name} credential has no "${needed}" capability`)
    }
  })
}
