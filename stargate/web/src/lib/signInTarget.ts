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
 * True when a finished sign-in may be handed to `raw`: an http(s) URL whose
 * host is the cookie domain or one of its subdomains, and https when the
 * deployment's session cookie is Secure (an http hand-off would arrive signed
 * out).
 */
export function isAllowedRedirect(raw: string, config: SignInConfig): boolean {
  if (/[\r\n\t]/.test(raw)) return false
  let url: URL
  try {
    url = new URL(raw)
  } catch {
    return false
  }
  if (url.protocol !== 'https:' && url.protocol !== 'http:') return false
  if (url.username !== '' || url.password !== '') return false
  if (config.require_https && url.protocol !== 'https:') return false
  const host = url.hostname.toLowerCase().replace(/\.$/, '')
  return config.redirect_hosts.some((allowed) => {
    const base = allowed.toLowerCase().replace(/^\.+|\.+$/g, '')
    return base !== '' && (host === base || host.endsWith(`.${base}`))
  })
}

let policy: SignInConfig | null = null
let policyRequest: Promise<SignInConfig> | null = null

/**
 * The server's hand-off policy, read once per page load. A failed read is not
 * cached: it yields the empty policy (only same-origin pages may be targets)
 * but the next caller asks again.
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
      .catch(() => ({ redirect_hosts: [], require_https: false }))
      .finally(() => {
        policyRequest = null
      })
  }
  return policyRequest
}

export interface Landing {
  /** What to navigate to (or hand off to) once the person is signed in. */
  target: string
  /** True when the target leaves this console for another origin. */
  external: boolean
}

/**
 * Resolves the requested landing target, or null when it cannot be honoured.
 * A refusal is not an error state for the sign-in itself: the console falls
 * back to its own account page, and the person still signs in.
 */
export async function resolveLanding(raw: string): Promise<Landing | null> {
  if (!raw) return null
  if (isSameOriginPath(raw)) return { target: raw, external: false }
  const config = await signInConfig()
  if (isAllowedRedirect(raw, config)) return { target: raw, external: true }
  return null
}
