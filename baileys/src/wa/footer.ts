const JAKARTA_TIME_ZONE = 'Asia/Jakarta'

const formatter = new Intl.DateTimeFormat('en-CA', {
  timeZone: JAKARTA_TIME_ZONE,
  year: 'numeric',
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
  hourCycle: 'h23',
})

function jakartaTimestamp(at: Date): string {
  const parts = Object.fromEntries(formatter.formatToParts(at).map((p) => [p.type, p.value]))
  return `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}:${parts.second}`
}

/**
 * Appends a WIB timestamp so identical batch text never leaves as byte-for-byte identical
 * messages — WhatsApp's abuse detection treats that as a broadcast/spam signature no
 * matter how the recipients are addressed or how far apart the sends land.
 */
export function withJakartaFooter(text: string, at: Date = new Date()): string {
  return `${text}\n\n${jakartaTimestamp(at)} WIB`
}
