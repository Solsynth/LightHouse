import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import PendingLoginPrompt from '@/components/PendingLoginPrompt.vue'
import { authApi } from '@/lib/api'
import type { AuthChallenge } from '@/lib/api/types'
import { i18n } from '@/lib/i18n'
import { useSessionStore } from '@/stores/session'

vi.mock('@/lib/device', () => ({
  deviceIdentity: () => ({ id: 'this-browser', name: 'Test Browser' }),
}))

vi.mock('@/lib/api', () => ({
  authApi: {
    pendingChallenges: vi.fn<() => Promise<AuthChallenge[]>>(),
    approveChallenge: vi.fn<(id: string) => Promise<void>>(),
    declineChallenge: vi.fn<(id: string) => Promise<void>>(),
  },
}))

function challenge(overrides: Partial<AuthChallenge> = {}): AuthChallenge {
  return {
    id: 'challenge-1',
    step_remain: 0,
    step_total: 1,
    failed_attempts: 0,
    blacklist_factors: [],
    audiences: [],
    scopes: [],
    device_id: 'other-device',
    device_name: 'Pixel',
    platform: 3,
    account_id: 'account-1',
    ip_address: '203.0.113.9',
    ...overrides,
  }
}

async function mountPrompt() {
  const pinia = createPinia()
  const wrapper = mount(PendingLoginPrompt, {
    global: { plugins: [pinia, i18n] },
  })
  const session = useSessionStore(pinia)
  session.identity = { id: 'account-1', name: 'tester' } as never
  await flushPromises()
  return wrapper
}

describe('PendingLoginPrompt', () => {
  beforeEach(() => {
    vi.mocked(authApi.pendingChallenges).mockReset()
    vi.mocked(authApi.approveChallenge).mockReset().mockResolvedValue(undefined)
    vi.mocked(authApi.declineChallenge).mockReset().mockResolvedValue(undefined)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('shows a completed sign-in from another device and approves it', async () => {
    vi.mocked(authApi.pendingChallenges).mockResolvedValue([challenge()])
    const wrapper = await mountPrompt()

    expect(wrapper.text()).toContain('Pixel')
    expect(wrapper.text()).toContain('203.0.113.9')

    await wrapper.get('.btn-primary').trigger('click')
    await flushPromises()

    expect(authApi.approveChallenge).toHaveBeenCalledWith('challenge-1')
    expect(wrapper.find('dialog').exists()).toBe(false)
  })

  it('denies the sign-in through the danger action', async () => {
    vi.mocked(authApi.pendingChallenges).mockResolvedValue([challenge()])
    const wrapper = await mountPrompt()

    await wrapper.get('.btn-error').trigger('click')
    await flushPromises()

    expect(authApi.declineChallenge).toHaveBeenCalledWith('challenge-1')
  })

  it('leaves the challenge that started in this browser alone', async () => {
    vi.mocked(authApi.pendingChallenges).mockResolvedValue([
      challenge({ device_id: 'this-browser' }),
    ])
    const wrapper = await mountPrompt()

    expect(wrapper.find('dialog').exists()).toBe(false)
  })

  it('stays silent when the list cannot be read', async () => {
    vi.mocked(authApi.pendingChallenges).mockRejectedValue(new Error('offline'))
    const wrapper = await mountPrompt()

    expect(wrapper.find('dialog').exists()).toBe(false)
  })
})
