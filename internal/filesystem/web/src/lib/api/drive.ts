import {
  authHeaders,
  buildUrl,
  DriveError,
  raiseUnauthorized,
  request,
  requestPage,
} from './client'
import { translate } from '@/lib/i18n'
import type {
  AccountIdentity,
  ACLEntryInput,
  BreadcrumbItem,
  CloudFile,
  FileACLDetail,
  FilePool,
  FileOrder,
  ListQuery,
  PoolUsage,
  QuotaSummary,
  UsageSummary,
} from './types'

function listQuery(options: ListQuery): Record<string, string | number | boolean> {
  const query: Record<string, string | number | boolean> = {
    take: options.take ?? 50,
  }
  if (options.offset) query.offset = options.offset
  if (options.query?.trim()) query.query = options.query.trim()
  if (options.order) query.order = options.order
  if (options.orderDesc !== undefined) query.orderDesc = options.orderDesc
  if (options.pool_id) query.pool_id = options.pool_id
  if (options.is_folder !== undefined) query.is_folder = options.is_folder
  if (options.recycled !== undefined) query.recycled = options.recycled
  if (options.indexed !== undefined) query.indexed = options.indexed
  return query
}

export const driveApi = {
  account: () => request<AccountIdentity>('/account/me'),

  quota: () => request<QuotaSummary>('/billing/quota'),
  usage: () => request<UsageSummary>('/billing/usage'),
  pools: () => requestPage<FilePool>('/pools'),
  poolUsage: (poolId: string) => request<PoolUsage>(`/billing/usage/${poolId}`),

  root: (options: ListQuery = {}, signal?: AbortSignal) =>
    requestPage<CloudFile>('/files/root/children', listQuery(options), signal),

  children: (folderId: string, options: ListQuery = {}, signal?: AbortSignal) =>
    requestPage<CloudFile>(`/files/${folderId}/children`, listQuery(options), signal),

  owned: (options: ListQuery = {}, signal?: AbortSignal) =>
    requestPage<CloudFile>('/files/me', listQuery(options), signal),

  unindexed: (options: ListQuery = {}, signal?: AbortSignal) =>
    requestPage<CloudFile>('/files/unindexed', listQuery(options), signal),

  file: (fileId: string, signal?: AbortSignal) =>
    request<CloudFile>(`/files/${fileId}/info`, { signal }),

  breadcrumb: (fileId: string, signal?: AbortSignal) =>
    request<BreadcrumbItem[]>(`/files/${fileId}/breadcrumb`, { signal }),

  createFolder: (name: string, parentId?: string | null) =>
    request<CloudFile>('/files/folders', {
      method: 'POST',
      body: { name, parent_id: parentId || undefined },
    }),

  rename: (fileId: string, name: string) =>
    request<CloudFile>(`/files/${fileId}`, { method: 'PATCH', body: { name } }),

  move: (fileIds: string[], parentId: string | null, indexed = true) =>
    request<{ count: number }>('/files/move/batch', {
      method: 'POST',
      body: { file_ids: fileIds, parent_id: parentId, indexed },
    }),

  recycle: (fileIds: string[]) =>
    request<{ count: number }>('/files/recycle/batch', {
      method: 'POST',
      body: { file_ids: fileIds },
    }),

  restore: (fileIds: string[]) =>
    request<{ count: number }>('/files/restore/batch', {
      method: 'POST',
      body: { file_ids: fileIds },
    }),

  purge: (fileIds: string[]) =>
    request<{ count: number }>('/files/delete/batch', {
      method: 'POST',
      body: { file_ids: fileIds },
    }),

  acl: (fileId: string, signal?: AbortSignal) =>
    request<FileACLDetail>(`/files/${fileId}/acl`, { signal }),

  saveACL: (fileId: string, body: { inherit_acl?: boolean; entries: ACLEntryInput[] }) =>
    request<FileACLDetail>(`/files/${fileId}/acl`, { method: 'PUT', body }),

  sharedWithMe: (options: ListQuery = {}, signal?: AbortSignal) =>
    requestPage<CloudFile>('/files/shared-with-me', listQuery(options), signal),
}

export function openUrl(fileId: string, download = false): string {
  return buildUrl(`/files/${fileId}/open`, download ? { download: 1 } : undefined)
}

/** Clean public address; the server only serves files marked public there. */
export function publicFileUrl(fileId: string): string {
  return `/files/${encodeURIComponent(fileId)}`
}

export function thumbnailUrl(fileId: string): string {
  return buildUrl(`/files/${fileId}`, { thumbnail: 1 })
}

export function previewUrl(fileId: string): string {
  return buildUrl(`/files/${fileId}`, {
    width: 1200,
    height: 900,
    fit: 'inside',
    format: 'webp',
  })
}

/**
 * Reads the head of a text-like file for the in-tab source preview.
 *
 * The API hands back a short-lived signed storage URL, which is then fetched
 * without credentials. Following `/open` with a credentialed fetch would fail
 * the CORS check the moment storage answers with `Access-Control-Allow-Origin:
 * *`; the signature already authorizes the read, so the cookie stays on the
 * API origin.
 */
export async function fetchTextPreview(fileId: string, limit = 256 * 1024): Promise<string> {
  const link = await request<{ url: string }>(`/files/${fileId}/url`)
  if (!link?.url) {
    throw new DriveError(502, translate('apiError.readFailed'))
  }

  let response: Response
  try {
    response = await fetch(link.url, { credentials: 'omit', mode: 'cors' })
  } catch {
    throw new DriveError(0, translate('apiError.readFailed'))
  }
  if (!response.ok) {
    throw new DriveError(
      response.status,
      response.status === 403
        ? translate('apiError.cannotReadBytes')
        : translate('apiError.readFailed'),
    )
  }
  const body = await response.text()
  if (body.length <= limit) return body
  return `${body.slice(0, limit)}\n\n… truncated at ${Math.round(limit / 1024)} KB`
}

export interface UploadOptions {
  parentId?: string | null
  /**
   * Whether the new file joins the drive's folder tree. A picker's uploads are
   * unindexed: the app keeps the id, and the person's drive does not acquire a
   * copy of every file they hand to another app. Unindexed rows still carry
   * their own bytes and appear in `/files/unindexed`.
   */
  indexed?: boolean
  onProgress?: (percent: number) => void
  signal?: AbortSignal
}

/**
 * Uploads through the storage's presigned PUT URL and commits the task.
 *
 * This is the fast path: the bytes go from the browser to the bucket, and the
 * API only signs the URLs and verifies the completed object. Large files use a
 * multipart session so parts can travel concurrently and a retry only repeats
 * the parts that are missing. Pools that cannot sign direct writes answer
 * `use_proxied_upload`, and `uploadDirect` falls back to the proxy below.
 */
export function uploadDirect(file: File, options: UploadOptions = {}): Promise<CloudFile> {
  return uploadPrepared(file, options).catch(async (caught: unknown) => {
    if (options.signal?.aborted) throw caught
    if (!canUseProxy(caught)) throw caught
    options.onProgress?.(0)
    return uploadProxied(file, options)
  })
}

/** Above this size, multipart parallelism beats one long PUT. */
const MULTIPART_THRESHOLD = 64 * 1024 * 1024
const PART_UPLOAD_CONCURRENCY = 3

interface PreparedUpload {
  task_id: string
  upload_url?: string
  upload_id?: string
  part_size?: number
  part_count?: number
  uploaded_parts?: number[]
  content_type?: string
}

interface PresignedPart {
  upload_url: string
}

/**
 * The server's explicit fallback hint, plus the failures that mean the fast
 * path is unavailable rather than the upload itself being invalid: an older
 * API without the route, a storage host the browser cannot reach (CORS,
 * offline), or a server-side failure. A 4xx validation or permission answer
 * is left alone — the proxy would refuse it for the same reason.
 */
function canUseProxy(caught: unknown): boolean {
  if (!(caught instanceof DriveError)) return false
  return (
    caught.useProxiedUpload ||
    caught.status === 0 ||
    caught.status === 404 ||
    caught.status === 405 ||
    caught.status >= 500
  )
}

async function uploadPrepared(file: File, options: UploadOptions): Promise<CloudFile> {
  const contentType = file.type || 'application/octet-stream'
  // One controller per attempt: a failed part must stop its siblings before
  // the proxy retry starts, not leave them writing into an abandoned task.
  const controller = new AbortController()
  const forwardAbort = () => controller.abort()
  if (options.signal?.aborted) controller.abort()
  else options.signal?.addEventListener('abort', forwardAbort, { once: true })
  const signal = controller.signal

  let taskId = ''
  try {
    const prepared = await request<PreparedUpload>('/files/upload/prepare', {
      method: 'POST',
      body: {
        file_name: file.name,
        file_size: file.size,
        content_type: contentType,
        index: options.indexed !== false,
        ...(options.parentId ? { parent_id: options.parentId } : {}),
        ...(file.size > MULTIPART_THRESHOLD ? { multipart: true } : {}),
      },
      signal,
    })
    taskId = prepared.task_id
    if (!taskId) throw new DriveError(502, translate('apiError.unreadableRecord'))

    if (prepared.upload_url) {
      await putBlob(
        prepared.upload_url,
        file,
        prepared.content_type || contentType,
        (loaded) => reportProgress(options, loaded, file.size),
        signal,
      )
    } else if (
      prepared.upload_id &&
      prepared.part_size &&
      prepared.part_size > 0 &&
      prepared.part_count &&
      prepared.part_count > 0
    ) {
      await uploadParts(file, taskId, prepared, options, signal)
    } else {
      throw new DriveError(502, translate('apiError.unreadableRecord'))
    }

    const completed = await request<CloudFile>(
      `/files/upload/${encodeURIComponent(taskId)}/complete`,
      { method: 'POST', signal },
    )
    options.onProgress?.(100)
    return completed
  } catch (caught) {
    controller.abort()
    if (taskId) await cancelUpload(taskId)
    throw caught
  } finally {
    options.signal?.removeEventListener('abort', forwardAbort)
  }
}

async function uploadParts(
  file: File,
  taskId: string,
  prepared: PreparedUpload,
  options: UploadOptions,
  signal: AbortSignal,
): Promise<void> {
  const partSize = prepared.part_size ?? 0
  const partCount = prepared.part_count ?? 0
  if (partSize <= 0 || partCount <= 0) {
    throw new DriveError(502, translate('apiError.unreadableRecord'))
  }

  const uploaded = new Set(prepared.uploaded_parts ?? [])
  const partBytes = (part: number) =>
    Math.max(0, Math.min(partSize, file.size - (part - 1) * partSize))
  let movedBytes = 0
  for (const part of uploaded) movedBytes += partBytes(part)

  const missing: number[] = []
  for (let part = 1; part <= partCount; part += 1) {
    if (!uploaded.has(part)) missing.push(part)
  }

  let cursor = 0
  const uploadPart = async () => {
    while (cursor < missing.length) {
      const part = missing[cursor]
      cursor += 1
      if (part === undefined) return
      const signed = await request<PresignedPart>(
        `/files/upload/${encodeURIComponent(taskId)}/part`,
        { method: 'POST', body: { part_number: part }, signal },
      )
      const start = (part - 1) * partSize
      // Multipart part URLs are signed without a content type, so the slice
      // must not add one.
      const blob = file.slice(start, Math.min(start + partSize, file.size), '')
      await putBlob(
        signed.upload_url,
        blob,
        null,
        (loaded) => reportProgress(options, movedBytes + loaded, file.size),
        signal,
      )
      movedBytes += blob.size
      reportProgress(options, movedBytes, file.size)
    }
  }

  const workers = Math.min(PART_UPLOAD_CONCURRENCY, missing.length)
  await Promise.all(Array.from({ length: workers }, uploadPart))
}

function reportProgress(options: UploadOptions, loaded: number, total: number): void {
  if (total <= 0) {
    options.onProgress?.(100)
    return
  }
  options.onProgress?.(Math.min(100, Math.round((loaded / total) * 100)))
}

/** PUT one blob at a presigned URL, reporting bytes the browser has sent. */
function putBlob(
  url: string,
  body: Blob,
  contentType: string | null,
  onProgress: (loaded: number) => void,
  signal: AbortSignal,
): Promise<void> {
  const { promise, resolve, reject } = Promise.withResolvers<void>()
  const xhr = new XMLHttpRequest()
  xhr.open('PUT', url)
  xhr.withCredentials = false
  if (contentType) xhr.setRequestHeader('Content-Type', contentType)

  const abort = () => xhr.abort()
  signal.addEventListener('abort', abort, { once: true })
  const settle = (fn: () => void) => {
    signal.removeEventListener('abort', abort)
    fn()
  }

  xhr.upload.addEventListener('progress', (event) => {
    if (!event.lengthComputable) return
    onProgress(Math.min(event.loaded, body.size))
  })
  xhr.addEventListener('load', () => {
    settle(() => {
      if (xhr.status >= 200 && xhr.status < 300) {
        onProgress(body.size)
        resolve()
        return
      }
      reject(new DriveError(xhr.status, xhr.statusText || translate('apiError.uploadFailed')))
    })
  })
  xhr.addEventListener('error', () => {
    settle(() => reject(new DriveError(0, translate('apiError.connectionDropped'))))
  })
  xhr.addEventListener('abort', () => {
    settle(() => reject(new DriveError(0, translate('apiError.uploadCancelled'))))
  })
  xhr.send(body)
  return promise
}

async function cancelUpload(taskId: string): Promise<void> {
  try {
    await request(`/files/upload/${encodeURIComponent(taskId)}`, { method: 'DELETE' })
  } catch {
    // The task expires on its own; a failed cancel must not hide the real error.
  }
}

/**
 * The compatibility path: one multipart POST through the API, which stages the
 * bytes and writes them to storage itself. It works with every pool, including
 * ones whose bucket cannot be written from a browser.
 */
function uploadProxied(file: File, options: UploadOptions): Promise<CloudFile> {
  const { promise, resolve, reject } = Promise.withResolvers<CloudFile>()
  const form = new FormData()
  form.set('file', file)
  form.set('index', String(options.indexed !== false))
  if (options.parentId) form.set('parent_id', options.parentId)
  const xhr = new XMLHttpRequest()
  xhr.open('POST', '/api/files/upload/direct')
  xhr.withCredentials = true
  xhr.setRequestHeader('Accept', 'application/json')
  for (const [name, value] of Object.entries(authHeaders())) {
    xhr.setRequestHeader(name, value)
  }

  const abort = () => xhr.abort()
  options.signal?.addEventListener('abort', abort, { once: true })

  xhr.upload.addEventListener('progress', (event) => {
    if (!event.lengthComputable || event.total === 0) return
    options.onProgress?.(Math.round((event.loaded / event.total) * 100))
  })

  xhr.addEventListener('load', () => {
    options.signal?.removeEventListener('abort', abort)
    if (xhr.status >= 200 && xhr.status < 300) {
      try {
        const parsed = JSON.parse(xhr.responseText) as CloudFile
        options.onProgress?.(100)
        resolve(parsed)
      } catch {
        reject(new DriveError(xhr.status, translate('apiError.unreadableRecord')))
      }
      return
    }
    let message = xhr.statusText || translate('apiError.uploadFailed')
    try {
      const payload = JSON.parse(xhr.responseText) as { error?: string; message?: string }
      message = payload.message || payload.error || message
    } catch {
      // Keep the HTTP status text.
    }
    if (xhr.status === 401) raiseUnauthorized()
    reject(new DriveError(xhr.status, message))
  })

  xhr.addEventListener('error', () => {
    options.signal?.removeEventListener('abort', abort)
    reject(new DriveError(0, translate('apiError.connectionDropped')))
  })

  xhr.addEventListener('abort', () => {
    options.signal?.removeEventListener('abort', abort)
    reject(new DriveError(0, translate('apiError.uploadCancelled')))
  })

  xhr.send(form)
  return promise
}

export type { FileOrder }
