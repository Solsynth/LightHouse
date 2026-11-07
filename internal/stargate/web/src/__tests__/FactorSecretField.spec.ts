import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import FactorSecretField from '@/components/FactorSecretField.vue'
import type { AuthFactor } from '@/lib/api/types'
import { i18n } from '@/lib/i18n'

function factor(type: number): AuthFactor {
  return { id: 'factor-1', type, trustworthy: 1, account_id: 'account-1' }
}

function mountField(type: number) {
  return mount(FactorSecretField, {
    props: { factor: factor(type) },
    global: { plugins: [i18n] },
  })
}

describe('FactorSecretField', () => {
  it('offers an authenticator code to the browser autofill', () => {
    const wrapper = mountField(3)
    const input = wrapper.get('input')

    expect(wrapper.get('label').text()).toBe('Authenticator code')
    expect(input.attributes('type')).toBe('text')
    expect(input.attributes('autocomplete')).toBe('one-time-code')
    expect(input.attributes('inputmode')).toBe('numeric')
    expect(input.attributes('maxlength')).toBe('6')
  })

  it('treats an emailed code the same way', () => {
    const wrapper = mountField(1)

    expect(wrapper.get('label').text()).toBe('Email code')
    expect(wrapper.get('input').attributes('autocomplete')).toBe('one-time-code')
  })

  it('keeps a password a password field', () => {
    const wrapper = mountField(0)
    const input = wrapper.get('input')

    expect(wrapper.get('label').text()).toBe('Password')
    expect(input.attributes('type')).toBe('password')
    expect(input.attributes('autocomplete')).toBe('current-password')
  })
})
