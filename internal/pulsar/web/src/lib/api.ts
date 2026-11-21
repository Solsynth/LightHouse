export class PulsarApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'PulsarApiError'
    this.status = status
    this.code = code
  }
}

export interface PageResult<T> {
  items: T[]
  total: number
}

export type Query = Record<string, string | number | boolean | undefined>

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  body?: unknown
  query?: Query
}

function buildUrl(path: string, query?: Query): string {
  if (!query) return path
  const params = new URLSearchParams()
  for (const [k, v] of Object.entries(query)) {
    if (v !== undefined && v !== '') params.set(k, String(v))
  }
  const qs = params.toString()
  return qs ? `${path}?${qs}` : path
}

export async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const res = await fetch(buildUrl(path, opts.query), {
    method: opts.method ?? 'GET',
    credentials: 'same-origin',
    headers: {
      Accept: 'application/json',
      ...(opts.body !== undefined ? { 'Content-Type': 'application/json' } : {}),
    },
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
  })
  if (!res.ok) {
    let code = 'HTTP_ERROR'
    let message = `Request failed (${res.status})`
    try {
      const errBody = (await res.json()) as { code?: string; message?: string }
      if (errBody.code) code = errBody.code
      if (errBody.message) message = errBody.message
    } catch {
      // ignore parse failure
    }
    throw new PulsarApiError(res.status, code, message)
  }
  if (res.status === 204) return undefined as T
  const text = await res.text()
  return (text ? JSON.parse(text) : undefined) as T
}

export async function requestList<T>(path: string, query?: Query): Promise<PageResult<T>> {
  const res = await fetch(buildUrl(path, query), {
    method: 'GET',
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    throw new PulsarApiError(res.status, 'HTTP_ERROR', `Request failed (${res.status})`)
  }
  const totalHeader = res.headers.get('X-Total')
  const body = (await res.json()) as T[] | { items?: T[]; total?: number }
  if (Array.isArray(body)) {
    return { items: body, total: totalHeader ? Number(totalHeader) : body.length }
  }
  const items = body.items ?? []
  return { items, total: body.total ?? (totalHeader ? Number(totalHeader) : items.length) }
}

export interface ActiveSession {
  id: string
  domain: string
  manual_id: string
  title: string
  subtitle?: string
  caption?: string
  artwork_url?: string
  progress_seconds: number
  duration_seconds: number
  is_paused: boolean
  client_name?: string
}

export interface Scrobble {
  id: string
  catalog_id?: string
  domain: string
  client_name: string
  title: string
  subtitle?: string
  caption?: string
  artwork_url?: string
  listened_seconds: number
  duration_seconds: number
  played_at: string
}

export interface TopEntity {
  name: string
  subtitle?: string
  count: number
  seconds: number
  artwork_url?: string
}

export interface MediaStats {
  account_id: string
  domain?: string
  total_scrobbles: number
  total_seconds: number
  top_items?: TopEntity[]
  top_creators?: TopEntity[]
  domain_breakdown: Record<string, number>
  days_active: number
}

export interface UnifiedMedia {
  id?: string
  urn: string
  domain: string
  provider: string
  title: string
  subtitle?: string
  caption?: string
  artwork_url?: string
  scrobble_count?: number
}

export interface Collection {
  id: string
  domain: string
  kind: string
  name: string
  description?: string
  is_public: boolean
  artwork_url?: string
  items?: CollectionItem[]
}

export interface CollectionItem {
  id: string
  collection_id: string
  catalog_id?: string
  urn?: string
  title: string
  subtitle?: string
  artwork_url?: string
  status?: string
  rating?: number
  score?: number
  note?: string
  notes?: string
  progress?: number
  volume_progress?: number
  catalog?: {
    id: string
    urn?: string
    title: string
    subtitle?: string
    artwork_url?: string
    domain: string
  }
}

export interface Review {
  id: string
  account_id: string
  catalog_id: string
  rating?: number
  body?: string
  created_at: string
  updated_at: string
}

export interface ReviewStats {
  catalog_id: string
  count: number
  average?: number
  distribution?: Record<number, number>
}

export interface CatalogItem {
  id: string
  urn: string
  domain: string
  provider: string
  provider_key: string
  title: string
  subtitle?: string
  caption?: string
  artwork_url?: string
  duration_seconds?: number
  created_at: string
}

export interface Creator {
  id: string
  name: string
  domain?: string
  role?: string
  item_count?: number
}

export const COLLECTION_STATUSES = [
  'plan_to_watch',
  'watching',
  'completed',
  'on_hold',
  'dropped',
] as const

export interface BangumiConnection {
  linked: boolean
  remote_user_id?: string
  remote_username?: string
  sync_enabled?: boolean
  conflict_policy?: string
  last_synced_at?: string
  last_error?: string
}

export function formatDuration(seconds: number): string {
  if (!seconds || seconds <= 0) return '0s'
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = Math.floor(seconds % 60)
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m`
  return `${s}s`
}

export function formatTimestamp(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

export const DOMAINS = ['music', 'anime', 'gaming', 'video', 'book'] as const
