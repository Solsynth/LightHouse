import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import OneTimeCodeField from '@/components/OneTimeCodeField.vue'

describe('OneTimeCodeField', () => {
  it('is labelled for code autofill', () => {
    const wrapper = mount(OneTimeCodeField, {
      props: { label: 'Authenticator code', modelValue: '' },
    })
    const input = wrapper.get('input')

    expect(wrapper.get('label').text()).toBe('Authenticator code')
    expect(wrapper.get('label').attributes('for')).toBe(input.attributes('id'))
    expect(input.attributes('autocomplete')).toBe('one-time-code')
    expect(input.attributes('inputmode')).toBe('numeric')
    expect(input.attributes('maxlength')).toBe('6')
    expect(input.attributes('pattern')).toBe('[0-9]*')
  })

  it('keeps only the digits a paste or autofill carries in', async () => {
    const wrapper = mount(OneTimeCodeField, {
      props: { label: 'Authenticator code', modelValue: '' },
    })
    const input = wrapper.get('input')

    await input.setValue('12a3 45678')

    expect(input.element.value).toBe('123456')
    const emitted = wrapper.emitted<string[]>('update:modelValue') ?? []
    expect(emitted.at(-1)).toEqual(['123456'])
  })

  it('honours a non-default code length', () => {
    const wrapper = mount(OneTimeCodeField, {
      props: { label: 'PIN', modelValue: '', digits: 4 },
    })

    expect(wrapper.get('input').attributes('maxlength')).toBe('4')
  })
})
