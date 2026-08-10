export interface ErrorBody {
  error_code: string
  message: string
  retryable: boolean
}

export class ApiError extends Error {
  readonly statusCode: number
  readonly errorCode: string
  readonly retryable: boolean
  readonly detail: string | undefined

  constructor(
    statusCode: number,
    errorCode: string,
    message: string,
    retryable: boolean,
    detail?: string,
  ) {
    super(message)
    this.name = 'ApiError'
    this.statusCode = statusCode
    this.errorCode = errorCode
    this.retryable = retryable
    this.detail = detail
  }

  toBody(): ErrorBody {
    return { error_code: this.errorCode, message: this.message, retryable: this.retryable }
  }
}

export const unauthorized = (message = 'missing or invalid bearer token'): ApiError =>
  new ApiError(401, 'unauthorized', message, false)

export const forbidden = (message = 'this credential may not perform that action'): ApiError =>
  new ApiError(403, 'forbidden', message, false)

export const notFound = (message = 'route not found'): ApiError =>
  new ApiError(404, 'not_found', message, false)

export const sessionNotFound = (id: string): ApiError =>
  new ApiError(404, 'session_not_found', `no session with id "${id}"`, false)

export const invalidPayload = (message: string): ApiError =>
  new ApiError(400, 'invalid_payload', message, false)

export const sessionNotConnected = (id: string, status: string): ApiError =>
  new ApiError(409, 'session_not_connected', `session "${id}" is ${status}, not connected`, true)

export const sessionLoggedOut = (id: string): ApiError =>
  new ApiError(409, 'session_logged_out', `session "${id}" is logged out and must be re-paired`, false)

export const notOnWhatsApp = (to: string): ApiError =>
  new ApiError(422, 'not_on_whatsapp', `${to} is not a WhatsApp user`, false)

export const sendFailed = (message: string, detail?: string): ApiError =>
  new ApiError(502, 'send_failed', message, true, detail)

export const rateLimitedByWa = (message = 'WhatsApp rate limited this send'): ApiError =>
  new ApiError(429, 'rate_limited_by_wa', message, true)

export const sendInProgress = (key: string): ApiError =>
  new ApiError(409, 'send_in_progress', `idempotency key "${key}" is already being sent`, true)

export const pairingFailed = (message: string, detail?: string): ApiError =>
  new ApiError(502, 'pairing_failed', message, true, detail)

export const upstreamTimeout = (operation: string, ms: number): ApiError =>
  new ApiError(504, 'upstream_timeout', `${operation} did not respond within ${ms}ms`, true)

export const payloadTooLarge = (message: string): ApiError =>
  new ApiError(413, 'payload_too_large', message, false)

export const unsupportedMediaType = (message: string): ApiError =>
  new ApiError(415, 'unsupported_media_type', message, false)
