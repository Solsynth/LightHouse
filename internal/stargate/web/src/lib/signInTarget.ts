import type { LocationQuery } from 'vue-router'

import { authApi } from '@/lib/api'
import type { SignInConfig } from '@/lib/api/types'

/**
 * Where a shared sign-in lands.
 *
 * Another Solar Network app reuses this console's login page by opening
 * `{siteUrl}/sign-in?redirectUri={its callback}` (or `?next={path}` for a page
 * of this console). The address is written by that app — and therefore by
 * anyone who can write a link — so it is honoured only when the server's
 * hand-off policy accepts it: the session cookie is scoped to the deployment's
 * cookie domain, and only origins under that domain both receive the session
 * and are allowed to be handed one.
 *
 * Nothing here decides policy on its own. The server answers the same question
 * in `/api/auth/sign-in/config` and applies the same rule when a social
 * provider round-trip reads `returnUrl`.
 */

/** Query keys a shared sign-in may carry; `redirectUri` wins when both appear. */
export const LANDING_KEYS = ['redirectUri', 'next'] as const

/** The target an address asks for, verbatim, or an empty string. */
export function requestedLanding(query: LocationQuery): string {
  for (const key of LANDING_KEYS) {
    const value = query[key]
    if (typeof value === 'string' && value) return value
  }
  return ''
}

/** Copies the landing parameters onto another address (create account, reset). */
export function landingQuery(query: LocationQuery): Record<string, string> {
  const out: Record<string, string> = {}
  for (const key of LANDING_KEYS) {
    const value = query[key]
    if (typeof value === 'string' && value) out[key] = value
  }
  return out
}

/**
 * True for a page of this console: a root-relative path. Protocol-relative
 * "//host" and control characters are refused, because either would turn a
 * client-side route push into a navigation somewhere else.
 */
export function isSameOriginPath(raw: string): boolean {
  if (!raw || !raw.startsWith('/') || raw.startsWith('//')) return false
  if (/[\r\n\t]/.test(raw)) return false
  try {
    const url = new URL(raw, window.location.origin)
    return url.origin === window.location.origin
  } catch {
    return false
  }
}

/**
 * The host a URL names, lowercased and without a trailing dot, or an empty
 * string when it is not an absolute http(s) URL. Credentials are refused here
 * because a "host" spelled after `@` is the attacker's, not the target's.
 */
function hostOf(raw: string, requireHttps: boolean): string {
  if (/[\r\n\t]/.test(raw)) return ''
  let url: URL
  try {
    url = new URL(raw)
  } catch {
    return ''
  }
  if (url.protocol !== 'https:' && url.protocol !== 'http:') return ''
  if (url.username !== '' || url.password !== '') return ''
  if (requireHttps && url.protocol !== 'https:') return ''
  return url.hostname.toLowerCase().replace(/\.$/, '')
}

/** True when `host` is one of `hosts` or a subdomain of one. */
function hostUnder(host: string, hosts: string[]): boolean {
  return hosts.some((allowed) => {
    const base = allowed.toLowerCase().replace(/^\.+|\.+$/g, '')
    return base !== '' && (host === base || host.endsWith(`.${base}`))
  })
}

/**
 * True when a finished sign-in may land on `raw` at all: an http(s) URL with no
 * credentials, on a cookie host or a hand-off host, and https when the session
 * cookie is Secure (an http target would arrive signed out).
 */
export function isAllowedRedirect(raw: string, config: SignInConfig): boolean {
  const host = hostOf(raw, config.require_https)
  if (host === '') return false
  return hostUnder(host, config.cookie_hosts) || hostUnder(host, config.handoff_hosts)
}

/**
 * True when the target shares the session cookie, so a plain navigation lands
 * there signed in. Asking the issuer for a hand-off code instead would be
 * refused — no client is registered for a host the cookie already reaches — so
 * this is the branch that keeps a subdomain app working.
 */
export function isCookieHostRedirect(raw: string, config: SignInConfig): boolean {
  const host = hostOf(raw, config.require_https)
  return host !== '' && hostUnder(host, config.cookie_hosts)
}

let policy: SignInConfig | null = null
let policyRequest: Promise<SignInConfig> | null = null

/**
 * The server's landing policy, read once per page load. A failed read is not
 * cached: it yields the empty policy (no target is accepted) but the next
 * caller asks again.
 */
export function signInConfig(): Promise<SignInConfig> {
  if (policy) return Promise.resolve(policy)
  if (!policyRequest) {
    policyRequest = authApi
      .signInConfig()
      .then((config) => {
        policy = config
        return config
      })
      .catch(() => ({ cookie_hosts: [], handoff_hosts: [], require_https: false }))
      .finally(() => {
        policyRequest = null
      })
  }
  return policyRequest
}

/**
 * How a finished sign-in reaches its target.
 *
 * `page` stays in this console, `shared` leaves for a host that already has the
 * session cookie, and `handoff` leaves for one that cannot have it — those are
 * three different trips, and using the hand-off for a cookie host is exactly
 * what turns a working shared sign-in into a refused redirect.
 */
export type LandingKind = 'page' | 'shared' | 'handoff'

export interface Landing {
  /** What to navigate to (or hand off to) once the person is signed in. */
  target: string
  kind: LandingKind
}

/**
 * Resolves the requested landing target, or null when it cannot be honoured.
 * A refusal is not an error state for the sign-in itself: the console falls
 * back to its own account page, and the person still signs in.
 */
export async function resolveLanding(raw: string): Promise<Landing | null> {
  if (!raw) return null
  if (isSameOriginPath(raw)) return { target: raw, kind: 'page' }
  const config = await signInConfig()
  if (!isAllowedRedirect(raw, config)) return null
  return { target: raw, kind: isCookieHostRedirect(raw, config) ? 'shared' : 'handoff' }
}

/**
 * The address to send the browser to for a hand-off that leaves this console.
 *
 * A target on another origin cannot receive the session cookie — a cookie
 * carries one Domain attribute and a browser refuses one that does not match
 * the request host — so it receives a single-use code instead, which its own
 * server trades for a token pair on its own domain.
 *
 * The code is requested here, after the person is signed in, so it is minted
 * for a live session and never sits in the address bar longer than one
 * navigation. It is short-lived (60s by default), so a page left open on the
 * sign-in screen cannot be replayed later.
 */
export async function handoffTargetUrl(target: string): Promise<string> {
  const response = await authApi.handoffSession({ redirect_uri: target })
  return appendCode(response.redirect_uri, response.code)
}

/** Appends the code as a query parameter, preserving any query already there. */
function appendCode(target: string, code: string): string {
  const url = new URL(target)
  url.searchParams.set('code', code)
  return url.toString()
}
