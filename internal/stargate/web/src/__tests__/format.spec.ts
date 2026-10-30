import { describe, expect, it } from 'vitest'

import { age, bytes, describeAgent, place, remaining, shortId, utcStamp } from '@/lib/format'

const iso = (offsetMs: number) => new Date(Date.now() + offsetMs).toISOString()

describe('remaining', () => {
  it('reads past timestamps as expired', () => {
    expect(remaining(iso(-1000))).toBe('expired')
  })

  it('keeps hours and minutes for sub-day windows', () => {
    expect(remaining(iso((3 * 60 + 12) * 60_000))).toBe('3h 12m')
    expect(remaining(iso((3 * 60 + 0) * 60_000))).toBe('3h')
  })

  it('uses whole days above one day', () => {
    expect(remaining(iso(5 * 24 * 60 * 60_000 + 60_000))).toBe('5d')
  })

  it('distinguishes a missing expiry', () => {
    expect(remaining(undefined)).toBe('no expiry')
  })
})

describe('age', () => {
  it('clamps future timestamps to pending', () => {
    expect(age(iso(60_000))).toBe('pending')
  })

  it('uses seconds below a minute', () => {
    expect(age(iso(-42_000))).toBe('42s')
  })
})

describe('describeAgent', () => {
  it('names the browser and version', () => {
    expect(
      describeAgent(
        'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36',
      ),
    ).toBe('Chrome 141')
  })

  it('falls back to a truncated raw value', () => {
    const agent = 'x'.repeat(60)
    expect(describeAgent(agent)).toBe(`${'x'.repeat(42)}…`)
  })
})

describe('scalars', () => {
  it('formats bytes across unit boundaries', () => {
    expect(bytes(0)).toBe('0 B')
    expect(bytes(1536)).toBe('1.5 KB')
  })

  it('shortens ids without dropping short ones', () => {
    expect(shortId('abc')).toBe('abc')
    expect(shortId('0123456789abcdef')).toBe('01234567')
  })

  it('renders a UTC stamp without a timezone suffix', () => {
    expect(utcStamp('2026-10-17T04:46:54Z')).toBe('2026-10-17 04:46:54Z')
  })

  it('joins city and country, or falls back to a dash', () => {
    expect(place({ city: 'Berlin', country: 'Germany' })).toBe('Berlin, Germany')
    expect(place(undefined)).toBe('—')
  })
})
