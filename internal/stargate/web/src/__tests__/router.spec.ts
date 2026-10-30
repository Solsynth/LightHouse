import { describe, expect, it } from 'vitest'

import router from '@/router/index'

/**
 * The emailed magic-spell links carry the word in the path, and Stargate mints
 * that URL from its own site origin. If this route is ever removed or renamed,
 * every verification and password-reset email dead-ends on the not-found page —
 * a failure no unit test of the view would catch.
 */
describe('magic-spell link', () => {
  const resolve = (path: string) => router.resolve(path)

  it('resolves the URL the emails link to', () => {
    const match = resolve('/spells/abcXYZ123')
    expect(match.name).toBe('spell')
    expect(match.params.word).toBe('abcXYZ123')
  })

  it('stays reachable without a session', () => {
    // The recipient may be signed out on a fresh browser; a guard that demanded
    // a session would bounce the link to sign-in before it could be applied.
    expect(resolve('/spells/abcXYZ123').meta.public).toBe(true)
  })

  it('keeps a word containing URL-safe punctuation intact', () => {
    expect(resolve('/spells/aB9%2Bc').params.word).toBe('aB9+c')
  })
})



/**
 * Friend-request pushes carry this action URI, and the fleet declares the same
 * path as an app link. A route rename would send every notification to the
 * not-found page, which no test of the view itself would catch.
 */
describe('friend request deep link', () => {
  const match = router.resolve('/account/relationships')

  it('resolves the path pushes and app links use', () => {
    expect(match.name).toBe('relationships')
  })

  it('is gated on the relationships module', () => {
    expect(match.meta.modules).toContain('app.solian.id.relationships')
  })

  it('requires a session', () => {
    expect(match.meta.public).toBeUndefined()
  })
})
