import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { request } from '@/lib/api/client'
import type { AuthChallenge, AuthFactor, TokenPair } from '@/lib/api/types'
import { useSignInStore } from '@/stores/signIn'

vi.mock('@/lib/device', () => ({
  deviceIdentity: () => ({ id: 'this-browser', name: 'Test Browser' }),
}))

vi.mock('@/lib/api/client', () => ({
  request: vi.fn<(path: string) => Promise<unknown>>(),
}))

const requestMock = vi.mocked(request)

function challenge(overrides: Partial<AuthChallenge> = {}): AuthChallenge {
  return {
    id: 'challenge-1',
    step_remain: 1,
    step_total: 2,
    failed_attempts: 0,
    blacklist_factors: [],
    audiences: [],
    scopes: [],
    device_id: 'this-browser',
    platform: 1,
    account_id: 'account-1',
    ...overrides,
  }
}

function factor(): AuthFactor {
  return {
    id: 'factor-1',
    type: 1,
    enabled_at: new Date().toISOString(),
  } as AuthFactor
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((settle) => {
    resolve = settle
  })
  return { promise, resolve }
}

/**
 * A challenge read can land after the attempt it belongs to has already been
 * reset or redeemed. Adopting that answer would put an empty "continue" screen
 * back on top of a completed sign-in, and its step count would drive a second
 * exchange of the same single-use code — which the server answers with
 * "Challenge has expired."
 */
describe('sign-in polling', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    requestMock.mockReset()
  })

  it('does not resurrect a challenge reset while its read was in flight', async () => {
    const store = useSignInStore()
    store.challenge = challenge()
    const read = deferred<AuthChallenge>()
    requestMock.mockImplementationOnce((() => read.promise) as typeof request)

    const polling = store.poll()
    store.reset()
    read.resolve(challenge())
    await polling

    expect(store.challenge).toBeNull()
    expect(store.factors).toEqual([])
  })

  it('does not re-adopt a challenge whose code was redeemed mid-read', async () => {
    const store = useSignInStore()
    const live = challenge()
    store.challenge = live
    const read = deferred<AuthChallenge>()
    const pair = { token: 'access', refresh_token: 'refresh', expires_in: 60 } as TokenPair
    requestMock
      .mockImplementationOnce((() => read.promise) as typeof request)
      .mockResolvedValueOnce(pair)

    const polling = store.poll()
    await store.exchange()
    read.resolve(challenge({ step_remain: 0, expired_at: new Date().toISOString() }))
    await polling

    expect(store.challenge).toEqual(live)
    expect(store.factors).toEqual([])
    // Only the read and the redemption: the stale answer loaded no factors.
    expect(requestMock).toHaveBeenCalledTimes(2)
  })

  it('stops reading once the challenge is complete', async () => {
    const store = useSignInStore()
    store.challenge = challenge({ step_remain: 0 })

    await store.poll()

    expect(requestMock).not.toHaveBeenCalled()
  })

  it('adopts a fresh answer while the attempt is still live', async () => {
    const store = useSignInStore()
    store.challenge = challenge()
    const next = challenge({ step_remain: 1, blacklist_factors: ['factor-2'] })
    const list: AuthFactor[] = [factor(), { ...factor(), id: 'factor-2' }]
    requestMock
      .mockResolvedValueOnce(next)
      .mockResolvedValueOnce(list)

    await store.poll()

    expect(store.challenge).toEqual(next)
    expect(store.factors.map((entry) => entry.id)).toEqual(['factor-1'])
  })
})
