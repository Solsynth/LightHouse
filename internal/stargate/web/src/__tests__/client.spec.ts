import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { onElevationRequired, request, StargateError } from '@/lib/api/client'

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('request', () => {
  const fetchMock = vi.fn<typeof fetch>()

  beforeEach(() => {
    vi.stubGlobal('fetch', fetchMock)
    fetchMock.mockReset()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('surfaces the API error code, message and field errors', async () => {
    fetchMock.mockResolvedValueOnce(
      jsonResponse(
        {
          code: 'VALIDATION_ERROR',
          message: 'The request data is invalid.',
          status: 400,
          errors: { password: ['At least 4 characters.'] },
        },
        400,
      ),
    )

    const failure = await request('/accounts').catch((error: unknown) => error)

    expect(failure).toBeInstanceOf(StargateError)
    expect((failure as StargateError).code).toBe('VALIDATION_ERROR')
    expect((failure as StargateError).fields?.password).toEqual(['At least 4 characters.'])
    expect((failure as StargateError).needsElevation).toBe(false)
  })

  it('opens elevation on AUTH_SUDO_REQUIRED and replays the request once', async () => {
    fetchMock
      .mockResolvedValueOnce(
        jsonResponse(
          { code: 'AUTH_SUDO_REQUIRED', message: 'This action requires re-authentication.', status: 403 },
          403,
        ),
      )
      .mockResolvedValueOnce(jsonResponse({ ok: true }))

    const elevate = vi.fn<() => Promise<void>>().mockResolvedValue(undefined)
    onElevationRequired(elevate)

    await expect(request('/factors', { method: 'POST', body: { type: 3 } })).resolves.toEqual({
      ok: true,
    })
    expect(elevate).toHaveBeenCalledTimes(1)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('refreshes once on 401 and replays the request', async () => {
    fetchMock
      .mockResolvedValueOnce(jsonResponse({ code: 'UNAUTHORIZED', message: 'Authentication is required.', status: 401 }, 401))
      .mockResolvedValueOnce(jsonResponse({ token: 'x' }))
      .mockResolvedValueOnce(jsonResponse({ name: 'operator' }))

    await expect(request('/accounts/me')).resolves.toEqual({ name: 'operator' })
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain('/api/auth/refresh')
  })

  it('does not loop when the replay is refused again', async () => {
    const refused = () =>
      jsonResponse(
        { code: 'AUTH_SUDO_REQUIRED', message: 'This action requires re-authentication.', status: 403 },
        403,
      )
    fetchMock.mockResolvedValueOnce(refused()).mockResolvedValueOnce(refused())

    onElevationRequired(vi.fn().mockResolvedValue(undefined))

    await expect(request('/factors', { method: 'POST' })).rejects.toMatchObject({
      code: 'AUTH_SUDO_REQUIRED',
    })
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })
})

describe('requestForm', () => {
  it('sends a form-urlencoded body and reads the JSON reply', async () => {
    const calls: RequestInit[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init: RequestInit) => {
        calls.push(init)
        return jsonResponse({ redirect_uri: 'https://client.example/cb?code=1' })
      }),
    )

    const { requestForm } = await import('@/lib/api/client')
    const form = new URLSearchParams({ client_id: 'app', authorize: 'true' })
    await expect(requestForm('/auth/open/authorize', form)).resolves.toEqual({
      redirect_uri: 'https://client.example/cb?code=1',
    })
    expect(new Headers(calls[0]?.headers).get('Content-Type')).toBe(
      'application/x-www-form-urlencoded',
    )
    expect(calls[0]?.body).toBe('client_id=app&authorize=true')
    vi.unstubAllGlobals()
  })
})

describe('OIDC error envelope', () => {
  it('maps error_description to the message', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        jsonResponse(
          { error: 'unauthorized_client', error_description: 'Client not found' },
          400,
        ),
      ),
    )
    const { request } = await import('@/lib/api/client')
    await expect(request('/auth/open/authorize')).rejects.toMatchObject({
      code: 'unauthorized_client',
      message: 'Client not found',
      status: 400,
    })
    vi.unstubAllGlobals()
  })
})
