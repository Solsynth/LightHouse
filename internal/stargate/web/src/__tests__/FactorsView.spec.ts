import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import FactorsView from '@/views/security/FactorsView.vue'
import { securityApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AuthFactor } from '@/lib/api/types'
import { i18n } from '@/lib/i18n'

vi.mock('qrcode', () => ({
  default: {
    toDataURL: vi.fn<() => Promise<string>>().mockResolvedValue('data:image/png;base64,qr'),
  },
}))

vi.mock('@/lib/api', () => ({
  securityApi: {
    factors: vi.fn<() => Promise<AuthFactor[]>>(),
    createFactor: vi.fn<(type: number, secret?: string) => Promise<AuthFactor>>(),
    enableFactor: vi.fn<(id: string, code?: string) => Promise<AuthFactor>>(),
    disableFactor: vi.fn<(id: string) => Promise<AuthFactor>>(),
    deleteFactor: vi.fn<(id: string) => Promise<void>>(),
    changePassword: vi.fn<(currentPassword: string, newPassword: string) => Promise<void>>(),
  },
}))

const factorsMock = vi.mocked(securityApi.factors)
const createFactorMock = vi.mocked(securityApi.createFactor)
const enableFactorMock = vi.mocked(securityApi.enableFactor)

function makeFactor(overrides: Partial<AuthFactor> = {}): AuthFactor {
  return {
    id: 'factor-1',
    created_at: '2026-11-01T00:00:00Z',
    account_id: 'acc-1',
    type: 5,
    trustworthy: 0,
    ...overrides,
  }
}

function mountView() {
  return mount(FactorsView, {
    global: {
      plugins: [createPinia(), i18n],
      stubs: {
        RouterLink: { template: '<a><slot /></a>' },
        SecurityNav: true,
        PageHeader: true,
      },
    },
  })
}

describe('FactorsView TOTP verification', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    factorsMock.mockResolvedValue([makeFactor({ id: 'rec-1', type: 5, enabled_at: '2026-11-01T00:00:00Z' })])
  })

  it('shows QR code, secret, and requires a 6-digit code to verify and enable a new TOTP factor', async () => {
    const totpFactor = makeFactor({
      id: 'totp-1',
      type: 3,
      trustworthy: 3,
      created_response: {
        secret: 'JBSWY3DPEHPK3PXP',
        uri: 'otpauth://totp/SolarNetwork:user?secret=JBSWY3DPEHPK3PXP&issuer=SolarNetwork&digits=6&period=30',
      },
    })
    createFactorMock.mockResolvedValue(totpFactor)
    enableFactorMock.mockResolvedValue({ ...totpFactor, enabled_at: '2026-11-01T00:01:00Z' })

    const wrapper = mountView()
    await flushPromises()

    const addBtn = wrapper.findAll('button').find((b) => b.text() === 'Add Factor')
    expect(addBtn).toBeDefined()
    await addBtn!.trigger('click')
    await flushPromises()

    expect(createFactorMock).toHaveBeenCalledWith(3, undefined)
    expect(wrapper.text()).toContain('JBSWY3DPEHPK3PXP')
    expect(wrapper.find('img[alt="Authenticator QR code"]').exists()).toBe(true)

    const verifyBtn = wrapper.findAll('button').find((b) => b.text() === 'Verify & Enable')
    expect(verifyBtn).toBeDefined()
    expect(verifyBtn!.attributes('disabled')).toBeDefined()

    const codeInput = wrapper.find('input[name="one-time-code"]')
    await codeInput.setValue('123456')
    expect(verifyBtn!.attributes('disabled')).toBeUndefined()

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(enableFactorMock).toHaveBeenCalledWith('totp-1', '123456')
    expect(wrapper.find('input[name="one-time-code"]').exists()).toBe(false)
  })

  it('keeps the verification form open and shows an error when the TOTP code is rejected', async () => {
    const totpFactor = makeFactor({
      id: 'totp-1',
      type: 3,
      trustworthy: 3,
      created_response: {
        secret: 'JBSWY3DPEHPK3PXP',
        uri: 'otpauth://totp/SolarNetwork:user?secret=JBSWY3DPEHPK3PXP&issuer=SolarNetwork&digits=6&period=30',
      },
    })
    createFactorMock.mockResolvedValue(totpFactor)
    enableFactorMock.mockRejectedValue(
      new StargateError({
        code: 'PADLOCK_AUTH_FACTOR_OPERATION_FAILED',
        message: 'Invalid factor code.',
        status: 400,
      }),
    )

    const wrapper = mountView()
    await flushPromises()

    await wrapper.findAll('button').find((b) => b.text() === 'Add Factor')!.trigger('click')
    await flushPromises()

    await wrapper.find('input[name="one-time-code"]').setValue('000000')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(wrapper.text()).toContain('Invalid factor code.')
    expect(wrapper.find('input[name="one-time-code"]').exists()).toBe(true)
  })

  it('prompts for a 6-digit code when enabling a disabled TOTP factor from the list', async () => {
    factorsMock.mockResolvedValue([
      makeFactor({ id: 'rec-1', type: 5, enabled_at: '2026-11-01T00:00:00Z' }),
      makeFactor({ id: 'totp-1', type: 3, trustworthy: 3 }),
    ])
    enableFactorMock.mockResolvedValue(makeFactor({ id: 'totp-1', type: 3, enabled_at: '2026-11-01T00:01:00Z' }))

    const wrapper = mountView()
    await flushPromises()

    const enableBtn = wrapper.find('button[aria-label="enable"]')
    expect(enableBtn.exists()).toBe(true)
    await enableBtn.trigger('click')
    await flushPromises()

    const modal = wrapper.find('dialog')
    expect(modal.exists()).toBe(true)
    await modal.find('input[name="one-time-code"]').setValue('654321')
    await modal.find('form').trigger('submit')
    await flushPromises()

    expect(enableFactorMock).toHaveBeenCalledWith('totp-1', '654321')
    expect(wrapper.find('dialog').exists()).toBe(false)
  })
})
