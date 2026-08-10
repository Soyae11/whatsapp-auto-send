export class InvalidPhoneNumberError extends Error {
  constructor(message: string) {
    super(message)
    this.name = 'InvalidPhoneNumberError'
  }
}

const MIN_DIGITS = 8
const MAX_DIGITS = 15

export function normalisePhoneNumber(input: string): string {
  const raw = input.trim()
  const digits = raw.replace(/\D/g, '')

  if (digits.length === 0) {
    throw new InvalidPhoneNumberError(`"${raw}" contains no digits`)
  }

  if (digits.startsWith('00')) {
    throw new InvalidPhoneNumberError(
      `"${raw}" starts with the 00 international prefix. Drop it and use the country code ` +
        `alone, e.g. 00628123456789 becomes 628123456789.`,
    )
  }

  if (digits.startsWith('0')) {
    throw new InvalidPhoneNumberError(
      `"${raw}" looks like a national number. WhatsApp needs the country code and no ` +
        `leading zero, e.g. Indonesian 081234567890 becomes 6281234567890.`,
    )
  }

  if (digits.length < MIN_DIGITS || digits.length > MAX_DIGITS) {
    throw new InvalidPhoneNumberError(
      `"${raw}" has ${digits.length} digits; a full international number has ` +
        `${MIN_DIGITS}–${MAX_DIGITS}.`,
    )
  }

  return digits
}

export function toUserJid(input: string): string {
  return `${normalisePhoneNumber(input)}@s.whatsapp.net`
}
