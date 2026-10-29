import type { ApiError } from './types'
import { translate } from '@/lib/i18n'

/** A typed failure from the Stargate API. */
export class StargateError extends Error {
  readonly code: string
  readonly status: number
  readonly detail?: string
  readonly fields?: Record<string, string[]>

  constructor(error: ApiError) {
    super(error.message)
    this.name = 'StargateError'
    this.code = error.code
    this.status = error.status
    this.detail = error.detail
    this.fields = error.errors
  }

  /** True when the session is missing or no longer valid. */
  get isUnauthenticated(): boolean {
    return this.status === 401
  }

  /** True when the action needs an elevation ("sudo") challenge. */
  get needsElevation(): boolean {
    return this.code === 'AUTH_SUDO_REQUIRED'
  }
}

type Method = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'

export type Query = Record<string, string | number | boolean | undefined>

export interface RequestOptions {
  method?: Method
  body?: unknown
  query?: Query
  signal?: AbortSignal
}

export interface Page<T> {
  items: T[]
  total: number
}

let elevationHandler: (() => Promise<void>) | null = null
let elevationInFlight = false
let refreshInFlight: Promise<boolean> | null = null

/**
 * Redeems the HttpOnly RefreshToken cookie for a fresh token pair.
 *
 * The server's own auto-renew only fires when it sees an expired AuthToken,
 * but browsers drop an expired cookie before sending it — so a cookie-based
 * client has to ask for the rotation explicitly. Concurrent 401s share one
 * refresh, which also matters because refresh tokens rotate.
 */
async function refreshSession(): Promise<boolean> {
  if (refreshInFlight) return refreshInFlight
  refreshInFlight = (async () => {
    try {
      const response = await fetch('/api/auth/refresh', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { Accept: 'application/json' },
      })
      return response.ok
    } catch {
      return false
    }
  })()
  try {
    return await refreshInFlight
  } finally {
    refreshInFlight = null
  }
}

/**
 * Installs the console's reaction to AUTH_SUDO_REQUIRED. The session store
 * opens the elevation sheet and resolves once a grant (or dismissal) settles;
 * the request that was refused is then retried exactly once.
 */
export function onElevationRequired(handler: () => Promise<void>) {
  elevationHandler = handler
}

/**
 * Reads a JSON body. Several endpoints answer 200/204 with no content (logout,
 * deletes), and parsing those as JSON would throw and abort the caller.
 */
async function readJson<T>(response: Response): Promise<T> {
  if (response.status === 204) return undefined as T
  const text = await response.text()
  if (!text) return undefined as T
  return JSON.parse(text) as T
}

function buildUrl(path: string, query?: Query): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query ?? {})) {
    if (value !== undefined && value !== '') params.set(key, String(value))
  }
  const suffix = params.size ? `?${params.toString()}` : ''
  return `/api${path}${suffix}`
}

async function parseError(response: Response): Promise<StargateError> {
  const fallback: ApiError = {
    code: `HTTP_${response.status}`,
    message: response.statusText || translate('errors.requestFailed'),
    status: response.status,
  }
  try {
    const payload = (await response.json()) as Partial<ApiError> & {
      // The OIDC endpoints answer with the RFC 6749 shape instead of the
      // house {code, message} envelope.
      error?: string
      error_description?: string
    }
    return new StargateError({
      ...fallback,
      ...payload,
      code: payload.code ?? payload.error ?? fallback.code,
      message: payload.message ?? payload.error_description ?? fallback.message,
    })
  } catch {
    return new StargateError(fallback)
  }
}

/**
 * One fetch with the console's retry rule: an AUTH_SUDO_REQUIRED answer opens
 * the elevation sheet, waits for it to settle, and replays the request once.
 */
async function send(url: string, init: RequestInit): Promise<Response> {
  let refreshed = false
  for (let attempt = 0; ; attempt += 1) {
    const response = await fetch(url, init)
    if (response.ok) return response
    const error = await parseError(response)

    if (error.status === 401 && !refreshed && !url.includes('/api/auth/refresh')) {
      refreshed = true
      if (await refreshSession()) continue
    }

    if (
      attempt === 0 &&
      error.needsElevation &&
      !elevationInFlight &&
      elevationHandler !== null
    ) {
      elevationInFlight = true
      try {
        await elevationHandler()
      } finally {
        elevationInFlight = false
      }
      continue
    }
    throw error
  }
}

/**
 * Sends one request to the Stargate API. The session travels in the HttpOnly
 * AuthToken cookie the server sets, so the console never stores tokens.
 */
export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (options.body !== undefined) headers['Content-Type'] = 'application/json'

  const response = await send(buildUrl(path, options.query), {
    method: options.method ?? 'GET',
    headers,
    credentials: 'same-origin',
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    signal: options.signal,
  })

  return readJson<T>(response)
}

/** Sends a form-urlencoded body (the OIDC authorize decision endpoint). */
export async function requestForm<T>(path: string, form: URLSearchParams): Promise<T> {
  const response = await send(buildUrl(path), {
    method: 'POST',
    headers: {
      'Content-Type': 'application/x-www-form-urlencoded',
      Accept: 'application/json',
    },
    credentials: 'same-origin',
    body: form.toString(),
  })
  return readJson<T>(response)
}

/** Reads a list plus the X-Total count the server sets for pagination. */
export async function requestPage<T>(path: string, query?: Query): Promise<Page<T>> {
  const response = await send(buildUrl(path, query), {
    headers: { Accept: 'application/json' },
    credentials: 'same-origin',
  })
  const items = (await readJson<T[] | null>(response)) ?? []
  return { items, total: Number(response.headers.get('X-Total') ?? items.length) }
}

/** Sends a request whose body is a bare JSON string (the device-label routes). */
export async function sendString(path: string, value: string, method: Method = 'PATCH'): Promise<void> {
  await send(buildUrl(path), {
    method,
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    credentials: 'same-origin',
    body: JSON.stringify(value),
  })
}
