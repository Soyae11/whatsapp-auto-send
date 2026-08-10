import { upstreamTimeout } from '../errors.js'

export async function withTimeout<T>(
  operation: string,
  ms: number,
  work: Promise<T>,
): Promise<T> {
  let timer: NodeJS.Timeout | undefined
  try {
    return await Promise.race([
      work,
      new Promise<never>((_resolve, reject) => {
        timer = setTimeout(() => reject(upstreamTimeout(operation, ms)), ms)
        timer.unref()
      }),
    ])
  } finally {
    clearTimeout(timer)
  }
}

export const WA_TIMEOUTS = {
  onWhatsApp: 15_000,
  sendMessage: 30_000,
  requestPairingCode: 20_000,
  logout: 10_000,
  fetchVersion: 15_000,
  presence: 5_000,
} as const
