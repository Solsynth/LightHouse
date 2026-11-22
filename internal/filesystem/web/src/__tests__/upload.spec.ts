import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { DriveError } from '@/lib/api/client'
import { uploadDirect } from '@/lib/api/drive'
import type { CloudFile } from '@/lib/api/types'

const file = (name: string, size: number, type = 'image/png'): File =>
  new File([new Uint8Array(size)], name, { type })

const uploaded = {
  id: 'file-1',
  name: 'shot.png',
  indexed: false,
} as CloudFile

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

type EventListenerFn = (event: {
  lengthComputable?: boolean
  loaded?: number
  total?: number
}) => void

class FakeXhr {
  static requests: FakeXhr[] = []
  static failPut = false

  method = ''
  url = ''
  headers: Record<string, string> = {}
  body: unknown = null
  status = 200
  statusText = 'OK'
  responseText = JSON.stringify(uploaded)
  withCredentials = false

  private listeners = new Map<string, EventListenerFn[]>()

  upload = {
    listeners: new Map<string, EventListenerFn[]>(),
    addEventListener: (type: string, fn: EventListenerFn) => {
      const list = this.upload.listeners.get(type) ?? []
      list.push(fn)
      this.upload.listeners.set(type, list)
    },
  }

  open(method: string, url: string) {
    this.method = method
    this.url = url
  }

  setRequestHeader(name: string, value: string) {
    this.headers[name] = value
  }

  addEventListener(type: string, fn: EventListenerFn) {
    const list = this.listeners.get(type) ?? []
    list.push(fn)
    this.listeners.set(type, list)
  }

  send(body: unknown) {
    this.body = body
    FakeXhr.requests.push(this)
    queueMicrotask(() => {
      if (this.method === 'PUT' && FakeXhr.failPut) {
        for (const fn of this.listeners.get('error') ?? []) fn({})
        return
      }
      const size = body instanceof Blob ? body.size : 1
      for (const fn of this.upload.listeners.get('progress') ?? []) {
        fn({ lengthComputable: true, loaded: size, total: size })
      }
      for (const fn of this.listeners.get('load') ?? []) fn({})
    })
  }

  abort() {
    for (const fn of this.listeners.get('abort') ?? []) fn({})
  }
}

function fetchFor(routes: Record<string, (init?: RequestInit) => Response>) {
  return vi.fn<typeof fetch>(async (input, init) => {
    const url = String(input)
    const route = routes[url]
    if (!route) throw new Error(`unmocked fetch ${url}`)
    return route(init)
  })
}

describe('direct upload', () => {
  beforeEach(() => {
    FakeXhr.requests = []
    FakeXhr.failPut = false
    vi.stubGlobal('XMLHttpRequest', FakeXhr)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('prepares, PUTs the file to storage, and completes the task', async () => {
    const fetchMock = fetchFor({
      '/api/files/upload/prepare': (init) => {
        expect(JSON.parse(String(init?.body))).toMatchObject({
          file_name: 'shot.png',
          file_size: 10,
          content_type: 'image/png',
          index: false,
        })
        return json({
          task_id: 'task-1',
          upload_url: 'https://s3.test/put',
          content_type: 'image/png',
        })
      },
      '/api/files/upload/task-1/complete': () => json(uploaded),
    })
    vi.stubGlobal('fetch', fetchMock)

    const progress: number[] = []
    await expect(
      uploadDirect(file('shot.png', 10), {
        indexed: false,
        onProgress: (percent) => progress.push(percent),
      }),
    ).resolves.toEqual(uploaded)

    const put = FakeXhr.requests.find((request) => request.method === 'PUT')
    expect(put?.url).toBe('https://s3.test/put')
    expect(put?.headers['Content-Type']).toBe('image/png')
    expect(put?.body).toBeInstanceOf(File)
    expect(progress.at(-1)).toBe(100)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('falls back to the proxy when the pool cannot sign direct writes', async () => {
    const fetchMock = fetchFor({
      '/api/files/upload/prepare': () =>
        json(
          { error: 'storage pool does not support direct uploads', use_proxied_upload: true },
          400,
        ),
    })
    vi.stubGlobal('fetch', fetchMock)

    await expect(uploadDirect(file('shot.png', 10), { indexed: false })).resolves.toEqual(uploaded)

    const proxy = FakeXhr.requests.find((request) => request.method === 'POST')
    expect(proxy?.url).toBe('/api/files/upload/direct')
    const form = proxy?.body as FormData
    expect(form.get('index')).toBe('false')
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('falls back when the bucket refuses the browser PUT', async () => {
    FakeXhr.failPut = true
    const cancelled: string[] = []
    vi.stubGlobal(
      'fetch',
      fetchFor({
        '/api/files/upload/prepare': () =>
          json({ task_id: 'task-3', upload_url: 'https://s3.test/put' }),
        '/api/files/upload/task-3': () => {
          cancelled.push('task-3')
          return new Response(null, { status: 204 })
        },
      }),
    )

    await expect(uploadDirect(file('shot.png', 10))).resolves.toEqual(uploaded)
    expect(cancelled).toEqual(['task-3'])
    expect(FakeXhr.requests.map((request) => request.method)).toContain('POST')
  })

  it('does not fall back on a validation failure', async () => {
    vi.stubGlobal(
      'fetch',
      fetchFor({
        '/api/files/upload/prepare': () => json({ error: 'file is too large' }, 400),
      }),
    )

    await expect(uploadDirect(file('shot.png', 10))).rejects.toBeInstanceOf(DriveError)
    expect(FakeXhr.requests).toHaveLength(0)
  })

  it('uploads multipart parts with their own presigned URLs', async () => {
    const partSize = 5 * 1024 * 1024
    // Crossing the multipart threshold, not the part size, selects the session.
    const size = 64 * 1024 * 1024 + 1
    const partCount = Math.ceil(size / partSize)
    const partNumbers = Array.from({ length: partCount }, (_, index) => index + 1)
    const parts: number[] = []
    const fetchMock = fetchFor({
      '/api/files/upload/prepare': (init) => {
        expect(JSON.parse(String(init?.body))).toMatchObject({
          file_size: size,
          multipart: true,
        })
        return json({
          task_id: 'task-2',
          upload_id: 'upload-1',
          part_size: partSize,
          part_count: partCount,
        })
      },
      '/api/files/upload/task-2/complete': () => json(uploaded),
      '/api/files/upload/task-2/part': (init?: RequestInit) => {
        const body = JSON.parse(String(init?.body)) as { part_number: number }
        parts.push(body.part_number)
        return json({ upload_url: `https://s3.test/part-${body.part_number}` })
      },
    })
    vi.stubGlobal('fetch', fetchMock)

    await expect(uploadDirect(file('large.png', size))).resolves.toEqual(uploaded)

    const puts = FakeXhr.requests
      .filter((request) => request.method === 'PUT')
      .sort(
        (left, right) => Number(left.url.split('-').at(-1)) - Number(right.url.split('-').at(-1)),
      )
    expect(parts.sort((left, right) => left - right)).toEqual(partNumbers)
    expect(puts.map((request) => request.url)).toEqual(
      partNumbers.map((part) => `https://s3.test/part-${part}`),
    )
    expect(puts.map((request) => (request.body as Blob).size)).toEqual(
      partNumbers.map((part) => Math.min(partSize, size - (part - 1) * partSize)),
    )
    expect(puts.every((request) => request.headers['Content-Type'] === undefined)).toBe(true)
  })
})
