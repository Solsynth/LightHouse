import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import SignInView from '@/views/SignInView.vue'
import { i18n } from '@/lib/i18n'
import { signInApi, authApi } from '@/lib/api'
import { request } from '@/lib/api/client'
import { getAssertion } from '@/lib/webauthn'
import { useSignInStore } from '@/stores/signIn'
import { useNoticeStore } from '@/stores/notice'
import type { AssertionParts } from '@/lib/webauthn'
import type { AuthChallenge, AuthFactor, PasskeyLoginStart } from '@/lib/api/types'

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ replace: vi.fn<(target: string) => Promise<void>>() }),
}))

vi.mock('@/lib/signInTarget', () => ({
  requestedLanding: () => '',
  resolveLanding: async () => ({ target: '/account', kind: 'page' as const }),
  handoffTargetUrl: async () => '',
  isAllowedRedirect: () => true,
  signInConfig: async () => ({
    cookie_hosts: [],
    handoff_hosts: [],
    require_https: false,
  }),
  landingQuery: () => ({}),
}))

vi.mock('@/lib/webauthn', () => ({
  passkeysSupported: () => true,
  getAssertion: vi.fn<() => Promise<AssertionParts>>(),
  passkeyFailure: () => 'passkey failed',
}))

vi.mock('@/lib/device', () => ({
  deviceIdentity: () => ({ id: 'this-browser', name: 'Test Browser' }),
}))

vi.mock('@/lib/api', () => ({
  signInApi: {
    passkeyDiscoverableStart: vi.fn<() => Promise<PasskeyLoginStart>>(),
    passkeyDiscoverableComplete: vi.fn<() => Promise<AuthChallenge>>(),
    exchange: vi.fn<() => Promise<{ token: string; refresh_token: string; expires_in: number }>>(),
  },
  authApi: { providers: vi.fn<() => Promise<string[]>>() },
}))

vi.mock('@/lib/api/client', () => ({
  request: vi.fn<(path: string) => Promise<unknown>>(),
  onElevationRequired: vi.fn<() => void>(),
  StargateError: class StargateError extends Error {},
}))

const startMock = vi.mocked(signInApi.passkeyDiscoverableStart)
const completeMock = vi.mocked(signInApi.passkeyDiscoverableComplete)
const exchangeMock = vi.mocked(signInApi.exchange)
const providersMock = vi.mocked(authApi.providers)
const assertionMock = vi.mocked(getAssertion)
const requestMock = vi.mocked(request)

function challenge(overrides: Partial<AuthChallenge> = {}): AuthChallenge {
  return {
    id: 'challenge-1',
    step_remain: 1,
    step_total: 1,
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

function factor(overrides: Partial<AuthFactor> = {}): AuthFactor {
  return {
    id: 'factor-totp',
    type: 3,
    enabled_at: new Date().toISOString(),
    ...overrides,
  } as AuthFactor
}

const start: PasskeyLoginStart = {
  challenge: 'assertion-challenge',
  rp_id: 'localhost',
  allow_credentials: [],
  timeout: 60000,
  user_verification: 'required',
  auth_challenge_id: 'challenge-1',
}

/**
 * Pins the risky-passkey contract: the server may keep steps owed after a
 * passkey assertion (a passwordless account with several factors under a
 * risky assessment). The console must adopt that challenge and let the factor
 * picker finish it, never redeem a challenge the server still counts
 * unfinished, which answers "Challenge not yet completed" and strands the
 * person on the identify screen.
 */
describe('passkey sign-in', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    setActivePinia(createPinia())
    startMock.mockResolvedValue(start)
    providersMock.mockResolvedValue([])
    requestMock.mockResolvedValue(undefined)
    exchangeMock.mockResolvedValue({ token: 'access', refresh_token: 'refresh', expires_in: 60 })
    assertionMock.mockResolvedValue({
      credential_id: 'credential-1',
      client_data_json: 'client-data',
      authenticator_data: 'authenticator-data',
      signature: 'signature',
    })
  })

  async function signInWithPasskey(): Promise<VueWrapper> {
    const wrapper = mount(SignInView, {
      global: { plugins: [i18n], stubs: { RouterLink: true } },
    })
    await flushPromises()
    const tile = wrapper.findAll('button').find((button) => button.text() === 'Passkey')
    expect(tile).toBeDefined()
    await tile!.trigger('click')
    await flushPromises()
    return wrapper
  }

  it('adopts the remaining steps instead of redeeming an unfinished challenge', async () => {
    completeMock.mockResolvedValue(
      challenge({ step_remain: 1, step_total: 2, blacklist_factors: ['factor-passkey'] }),
    )
    requestMock.mockResolvedValueOnce([factor(), factor({ id: 'factor-passkey', type: 7 })])

    const wrapper = await signInWithPasskey()

    expect(completeMock).toHaveBeenCalledWith('challenge-1', expect.anything())
    expect(exchangeMock).not.toHaveBeenCalled()
    const store = useSignInStore()
    expect(store.challenge?.step_remain).toBe(1)
    expect(store.factors.map((entry) => entry.id)).toEqual(['factor-totp'])
    expect(wrapper.text()).toContain('1 of 2 steps remaining')
    expect(wrapper.text()).toContain('A rotating code from an authenticator app.')
    wrapper.unmount()
  })

  it('redeems a challenge the assertion settled', async () => {
    completeMock.mockResolvedValue(challenge({ step_remain: 0, step_total: 1 }))

    const wrapper = await signInWithPasskey()

    expect(exchangeMock).toHaveBeenCalledWith('challenge-1')
    expect(useNoticeStore().items.map((item) => item.message)).toContain(
      'Signed in with a passkey.',
    )
    wrapper.unmount()
  })
})
