import { afterEach, describe, expect, it, vi } from 'vitest'

import { isAllowedRedirect, isSameOriginPath, resolveLanding } from '@/lib/signInTarget'

/**
 * The address of a shared sign-in is written by the app that sends the person
 * here, and by anyone who can write a link. The console must refuse a target
 * before navigating away, or a crafted link turns the login page into an open
 * redirector — and a target outside the cookie's hosts would arrive signed out
 * anyway.
 */
describe('shared sign-in landing target', () => {
  const policy = { redirect_hosts: ['solian.app', 'id.solian.app'], require_https: true }

  it('accepts a cookie host and its subdomains', () => {
    for (const url of [
      'https://app.solian.app/oauth/callback',
      'https://a.b.solian.app/cb?x=1',
      'https://solian.app/',
      'https://id.solian.app/settings',
    ]) {
      expect(isAllowedRedirect(url, policy)).toBe(true)
    }
  })

  it('refuses a foreign, lookalike, or non-https target', () => {
    for (const url of [
      'https://evil.example/cb',
      'https://evil-solian.app/cb',
      'https://solian.app.evil.example/cb',
      'http://app.solian.app/cb',
      'javascript:alert(1)',
      'https://app.solian.app@evil.example/cb',
      'not a url',
    ]) {
      expect(isAllowedRedirect(url, policy)).toBe(false)
    }
  })

  it('allows http when the session cookie is not Secure', () => {
    expect(
      isAllowedRedirect('http://localhost:3001/cb', {
        redirect_hosts: ['localhost'],
        require_https: false,
      }),
    ).toBe(true)
  })

  it('keeps console pages same-origin only', () => {
    expect(isSameOriginPath('/account')).toBe(true)
    expect(isSameOriginPath('/security/connections?linked=github')).toBe(true)
    expect(isSameOriginPath('//evil.example/path')).toBe(false)
    expect(isSameOriginPath('https://app.solian.app/')).toBe(false)
    expect(isSameOriginPath('/account\nSet-Cookie: x=y')).toBe(false)
  })
})
/**
 * resolveLanding is what the page acts on: a console path stays a router push,
 * a cookie host becomes a full-page hand-off, and anything else yields null so
 * the caller falls back to the account page instead of navigating away.
 */
describe('resolveLanding', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.resetModules()
  })

  it('marks a console path as a router navigation', async () => {
    await expect(resolveLanding('/security/sessions')).resolves.toEqual({
      target: '/security/sessions',
      external: false,
    })
  })

  it('marks a cookie host as an external hand-off', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        new Response(
          JSON.stringify({ redirect_hosts: ['solian.app'], require_https: true }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
      ),
    )
    const { resolveLanding: resolve } = await import('@/lib/signInTarget')
    await expect(resolve('https://app.solian.app/oauth/callback?code=1')).resolves.toEqual({
      target: 'https://app.solian.app/oauth/callback?code=1',
      external: true,
    })
  })

  it('refuses a target outside the cookie hosts', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        new Response(
          JSON.stringify({ redirect_hosts: ['solian.app'], require_https: true }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
      ),
    )
    const { resolveLanding: resolve } = await import('@/lib/signInTarget')
    await expect(resolve('https://evil.example/cb')).resolves.toBeNull()
  })
})

/**
 * A hand-off that leaves this console gets a single-use code rather than the
 * session cookie, and the address the browser is sent to is the one the issuer
 * validated — not whatever the link asked for. The console must therefore use
 * the server's `redirect_uri` and append the code to it.
 */
describe('handoffTargetUrl', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.resetModules()
  })

  it('lands on the issuer-validated target with the code appended', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        new Response(
          JSON.stringify({
            code: 'single-use-code',
            redirect_uri: 'https://dx.solsynth.dev/releases?page=2',
            expires_in: 60,
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
      ),
    )
    const { handoffTargetUrl } = await import('@/lib/signInTarget')
    const url = new URL(await handoffTargetUrl('https://dx.solsynth.dev/releases?page=2'))
    expect(url.origin + url.pathname).toBe('https://dx.solsynth.dev/releases')
    expect(url.searchParams.get('code')).toBe('single-use-code')
    // The target's own query survives; only the code is added.
    expect(url.searchParams.get('page')).toBe('2')
  })

  it('fails when the issuer refuses, rather than navigating signed out', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(JSON.stringify({ error: 'invalid_target' }), {
            status: 400,
            headers: { 'Content-Type': 'application/json' },
          }),
      ),
    )
    const { handoffTargetUrl } = await import('@/lib/signInTarget')
    await expect(handoffTargetUrl('https://evil.example/')).rejects.toThrow()
  })
})
