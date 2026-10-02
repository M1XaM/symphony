import { describe, expect, it } from 'vitest'
import { fmtBytes, fmtNum } from './format'

describe('fmtBytes', () => {
  it('keeps whole bytes and scales the rest', () => {
    expect(fmtBytes(0)).toBe('0 B')
    expect(fmtBytes(1023)).toBe('1023 B')
    expect(fmtBytes(1536)).toBe('1.5 KB')
    expect(fmtBytes(256 * 1048576)).toBe('256.0 MB')
    expect(fmtBytes(-1)).toBe('—')
    expect(fmtBytes(Number.NaN)).toBe('—')
  })
})

describe('fmtNum', () => {
  it('shows a dash for a missing value', () => {
    expect(fmtNum(undefined)).toBe('—')
    expect(fmtNum(Number.NaN, 2)).toBe('—')
    expect(fmtNum(12.345, 1)).toBe('12.3')
  })
})
