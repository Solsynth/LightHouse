import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  isAllowedRedirect,
  isCookieHostRedirect,
  isSameOriginPath,
  resolveLanding,
} from '@/lib/signInTarget'

/**
 * The address of a shared sign-in is written by the app that sends the person
 * here, and by anyone who can write a link. The console must refuse a target
 * before navigating away, or a crafted link turns the login page into an open
 * redirector — and a target outside the cookie's hosts would arrive signed out
 * anyway.
 *
 * The policy arrives split because the two allowed kinds are different trips:
 * a cookie host already has the session, a hand-off host has to be given a code.
 */
describe('shared sign-in landing target', () => {
  const policy = {
    cookie_hosts: ['solian.app', 'id.solian.app'],
    handoff_hosts: ['solsynth.dev'],
    require_https: true,
  }

  it('accepts a cookie host and its subdomains', () => {
    for (const url of [
      'https://app.solian.app/oauth/callback',
      'https://a.b.solian.app/cb?x=1',
      'https://solian.app/',
      'https://id.solian.app/settings',
      'https://web.solian.app/',
    ]) {
      expect(isAllowedRedirect(url, policy)).toBe(true)
    }
  })

  it('accepts a registered hand-off host', () => {
    expect(isAllowedRedirect('https://dx.solsynth.dev/releases', policy)).toBe(true)
  })

  it('refuses a foreign, lookalike, or non-https target', () => {
    for (const url of [
      'https://evil.example/cb',
      'https://evil-solian.app/cb',
      'https://solian.app.evil.example/cb',
      'https://solsynth.dev.evil.example/cb',
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
        cookie_hosts: ['localhost'],
        handoff_hosts: [],
        require_https: false,
      }),
    ).toBe(true)
  })

  it('tells the two trips apart by the host the target names', () => {
    expect(isCookieHostRedirect('https://web.solian.app/', policy)).toBe(true)
    expect(isCookieHostRedirect('https://dx.solsynth.dev/releases', policy)).toBe(false)
    expect(isCookieHostRedirect('https://evil.example/', policy)).toBe(false)
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
 * a cookie host becomes a plain navigation (the session is already there), a
 * hand-off host becomes a code exchange, and anything else yields null so the
 * caller falls back instead of navigating away.
 */
// The policy is cached per module instance; resetModules plus a dynamic import
// is the only way to give each case a fresh fetch, so the dynamic specifier is
// a module-loading boundary this suite exists to exercise.
describe('resolveLanding', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.resetModules()
  })

  const policyResponse = (body: unknown) =>
    vi.fn<() => Promise<Response>>(
      async () =>
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
    )

  it('marks a console path as a router navigation', async () => {
    await expect(resolveLanding('/security/sessions')).resolves.toEqual({
      target: '/security/sessions',
      kind: 'page',
    })
  })

  // The regression this pins: web.solian.app sits under the cookie domain, so
  // the session is already shared. Treating it as a hand-off asks the issuer to
  // mint a code for a host no client is registered for, and the sign-in reports
  // a refused redirect although the app was in fact signed in.
  it('lands a cookie subdomain by navigation, not by hand-off', async () => {
    vi.stubGlobal('fetch', policyResponse({
      cookie_hosts: ['solian.app', 'id.solian.app'],
      handoff_hosts: ['solsynth.dev'],
      require_https: true,
    }))
    const { resolveLanding: resolve } = await import('@/lib/signInTarget')
    await expect(resolve('https://web.solian.app/')).resolves.toEqual({
      target: 'https://web.solian.app/',
      kind: 'shared',
    })
  })

  it('marks a hand-off host as a code exchange', async () => {
    vi.stubGlobal('fetch', policyResponse({
      cookie_hosts: ['solian.app'],
      handoff_hosts: ['solsynth.dev'],
      require_https: true,
    }))
    const { resolveLanding: resolve } = await import('@/lib/signInTarget')
    await expect(resolve('https://dx.solsynth.dev/releases')).resolves.toEqual({
      target: 'https://dx.solsynth.dev/releases',
      kind: 'handoff',
    })
  })

  it('refuses a target outside both host lists', async () => {
    vi.stubGlobal('fetch', policyResponse({
      cookie_hosts: ['solian.app'],
      handoff_hosts: [],
      require_https: true,
    }))
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
