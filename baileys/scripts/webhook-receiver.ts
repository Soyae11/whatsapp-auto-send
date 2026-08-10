/**
 * A reference webhook consumer, for checking that what this service sends is what your Go
 * API will accept. It verifies the HMAC exactly as a real consumer should and refuses
 * anything that does not check out.
 *
 *   WEBHOOK_SECRET=$(grep ^WEBHOOK_SECRET .env | cut -d= -f2) npx tsx scripts/webhook-receiver.ts
 *
 * Then point the service at it. From inside docker-compose the host is reachable on the
 * bridge gateway:
 *
 *   docker network inspect baileys_default -f '{{(index .IPAM.Config 0).Gateway}}'
 *   WEBHOOK_URL=http://<that-ip>:4000/hook
 */
import { createServer } from 'node:http'
import { TIMESTAMP_HEADER, SIGNATURE_HEADER, EVENT_HEADER, verifySignature } from '../src/webhooks/signature.js'

const secret = process.env.WEBHOOK_SECRET
const port = Number(process.env.PORT ?? 4000)

if (!secret) {
  console.error('WEBHOOK_SECRET is required')
  process.exit(1)
}

/** Reject anything older than this — the timestamp is signed, so this blocks replay. */
const MAX_AGE_MS = 5 * 60_000

const server = createServer((req, res) => {
  const chunks: Buffer[] = []
  req.on('data', (chunk: Buffer) => chunks.push(chunk))
  req.on('end', () => {
    const body = Buffer.concat(chunks).toString('utf8')
    const timestamp = Number(req.headers[TIMESTAMP_HEADER])
    const header = String(req.headers[SIGNATURE_HEADER] ?? '')
    const signature = header.replace(/^sha256=/, '')
    const eventType = String(req.headers[EVENT_HEADER] ?? 'unknown')

    const fresh = Number.isFinite(timestamp) && Math.abs(Date.now() - timestamp) < MAX_AGE_MS
    const valid = fresh && verifySignature(secret, timestamp, body, signature)

    const mark = valid ? '[32m✓[0m' : '[31m✗[0m'
    console.log(`${mark} ${eventType}${fresh ? '' : ' (stale timestamp)'}`)
    console.log(`  ${body}`)

    res.writeHead(valid ? 200 : 401, { 'Content-Type': 'application/json' })
    res.end(JSON.stringify({ ok: valid }))
  })
})

server.listen(port, () => {
  console.log(`webhook receiver listening on http://0.0.0.0:${port}/hook`)
  console.log('verifying HMAC-SHA256 over `<timestamp>.<body>`\n')
})
