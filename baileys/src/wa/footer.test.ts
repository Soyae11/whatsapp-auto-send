import { describe, expect, it } from 'vitest'
import { withJakartaFooter } from './footer.js'

describe('withJakartaFooter', () => {
  it('converts a UTC instant to Asia/Jakarta (UTC+7, no DST)', () => {
    const at = new Date('2026-08-09T21:08:10.000Z')
    expect(withJakartaFooter('hi', at)).toBe('hi\n\n2026-08-10 04:08:10 WIB')
  })

  it('keeps the original text intact ahead of the footer', () => {
    const at = new Date('2026-01-01T00:00:00.000Z')
    const out = withJakartaFooter('line one\nline two', at)
    expect(out.startsWith('line one\nline two\n\n')).toBe(true)
  })

  it('never produces "24" for a Jakarta midnight hour', () => {
    // 17:00 UTC = 00:00 next day in Jakarta (UTC+7)
    const at = new Date('2026-03-04T17:00:00.000Z')
    expect(withJakartaFooter('x', at)).toBe('x\n\n2026-03-05 00:00:00 WIB')
  })

  it('produces a different footer for two sends a couple minutes apart', () => {
    const first = withJakartaFooter('same text', new Date('2026-08-09T21:08:10.000Z'))
    const second = withJakartaFooter('same text', new Date('2026-08-09T21:10:40.000Z'))
    expect(first).not.toBe(second)
  })
})
