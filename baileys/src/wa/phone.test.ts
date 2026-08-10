import { describe, expect, it } from 'vitest'
import { InvalidPhoneNumberError, normalisePhoneNumber, toUserJid } from './phone.js'

describe('normalisePhoneNumber', () => {
  it('strips the formatting people actually type', () => {
    for (const input of [
      '6281234567890',
      '+6281234567890',
      '+62 812-3456-7890',
      ' 62 812 3456 7890 ',
      '(62) 812.3456.7890',
    ]) {
      expect(normalisePhoneNumber(input), input).toBe('6281234567890')
    }
  })

  it('rejects a national number with a leading zero', () => {
    expect(() => normalisePhoneNumber('081234567890')).toThrow(InvalidPhoneNumberError)
    expect(() => normalisePhoneNumber('081234567890')).toThrow(/leading zero/)
  })

  it('rejects the 00 international prefix with a specific hint', () => {
    expect(() => normalisePhoneNumber('006281234567890')).toThrow(/00 international prefix/)
  })

  it('rejects numbers that are too short or too long', () => {
    expect(() => normalisePhoneNumber('62812')).toThrow(/digits/)
    expect(() => normalisePhoneNumber('6281234567890123')).toThrow(/digits/)
  })

  it('rejects input with no digits at all', () => {
    expect(() => normalisePhoneNumber('   ')).toThrow(/no digits/)
    expect(() => normalisePhoneNumber('not-a-number')).toThrow(/no digits/)
  })

  it('accepts the shortest and longest valid lengths', () => {
    expect(normalisePhoneNumber('12345678')).toBe('12345678')
    expect(normalisePhoneNumber('123456789012345')).toBe('123456789012345')
  })
})

describe('toUserJid', () => {
  it('appends the user server', () => {
    expect(toUserJid('+62 812-3456-7890')).toBe('6281234567890@s.whatsapp.net')
  })

  it('refuses to build a JID from an invalid number', () => {
    expect(() => toUserJid('081234567890')).toThrow(InvalidPhoneNumberError)
  })
})
