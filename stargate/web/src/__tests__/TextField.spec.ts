import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

import TextField from '@/components/TextField.vue'

describe('TextField', () => {
  it('floats the label once the field holds a value', async () => {
    const wrapper = mount(TextField, { props: { label: 'Account', modelValue: '' } })
    const root = wrapper.get('.field')

    expect(root.classes()).not.toContain('is-floated')

    await wrapper.setProps({ modelValue: 'littlesheep' })
    expect(root.classes()).toContain('is-floated')
  })

  it('keeps the label down for an empty field and floats it on focus', async () => {
    const wrapper = mount(TextField, { props: { label: 'Account', modelValue: '' } })
    const input = wrapper.get('input')

    expect(wrapper.get('.field').classes()).not.toContain('is-floated')
    await input.trigger('focus')
    // Focus does not float the label by itself: the caption stays on the value
    // line until there is a value, and only the indicator responds.
    expect(wrapper.get('.field').classes()).not.toContain('is-floated')
  })

  it('always floats a select label, since its first option is a real value', () => {
    const wrapper = mount(TextField, {
      props: { label: 'Status', as: 'select', modelValue: 1 },
      slots: { default: '<option value="1">Active</option>' },
    })

    expect(wrapper.get('.field').classes()).toContain('is-floated')
    expect(wrapper.get('select').element.value).toBe('1')
  })

  it('coerces a .number select back to a number', async () => {
    const wrapper = mount(TextField, {
      props: {
        label: 'Status',
        as: 'select',
        modelValue: 0,
        modelModifiers: { number: true },
      },
      slots: { default: '<option value="0">Draft</option><option value="1">Active</option>' },
    })

    await wrapper.get('select').setValue('1')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([1])
  })

  it('points the caption at the control it names', () => {
    const wrapper = mount(TextField, { props: { label: 'Account', modelValue: '' } })
    const label = wrapper.get('label')
    const input = wrapper.get('input')

    expect(label.attributes('for')).toBeTruthy()
    expect(label.attributes('for')).toBe(input.attributes('id'))
  })

  it("keeps a caller's id so an existing label still points at the control", () => {
    const wrapper = mount(TextField, {
      props: { label: 'Account', modelValue: '', id: 'account' },
    })

    expect(wrapper.get('input').attributes('id')).toBe('account')
    expect(wrapper.get('label').attributes('for')).toBe('account')
  })

  it('forwards attributes to the control rather than the wrapper', async () => {
    const wrapper = mount(TextField, {
      props: { label: 'Code', modelValue: '', inputmode: 'numeric', autocomplete: 'one-time-code' },
      attrs: { 'aria-label': 'Verification code' },
    })
    const input = wrapper.get('input')

    expect(input.attributes('inputmode')).toBe('numeric')
    expect(input.attributes('autocomplete')).toBe('one-time-code')
    expect(input.attributes('aria-label')).toBe('Verification code')
    expect(wrapper.get('.field').attributes('aria-label')).toBeUndefined()
  })

  it('emits on input and forwards keydown handlers to the control', async () => {
    const onKeydown = vi.fn<(event: KeyboardEvent) => void>()
    const wrapper = mount(TextField, {
      props: { label: 'Code', modelValue: '' },
      attrs: { onKeydown },
    })

    await wrapper.get('input').setValue('123456')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual(['123456'])

    await wrapper.get('input').trigger('keydown', { key: 'Enter' })
    expect(onKeydown).toHaveBeenCalled()
  })

  it('marks the control invalid and shows the hint in the error tone', () => {
    const wrapper = mount(TextField, {
      props: { label: 'Email', modelValue: 'nope', invalid: true, hint: 'Not an address' },
    })

    expect(wrapper.get('input').attributes('aria-invalid')).toBe('true')
    expect(wrapper.get('.field').classes()).toContain('is-invalid')
    expect(wrapper.get('.field-hint').classes()).toContain('field-hint-error')
    expect(wrapper.get('.field-hint').text()).toBe('Not an address')
  })

  it('reserves no caption line when it has no label', () => {
    // A label-less row (the inline verify fields) must keep the bare control
    // height: reserving the caption line would push its value below the button
    // beside it.
    const wrapper = mount(TextField, { props: { modelValue: '' } })

    expect(wrapper.find('label').exists()).toBe(false)
    expect(wrapper.get('.field').classes()).toContain('field-bare')
  })

  it('renders no caption in bare mode', async () => {
    const wrapper = mount(TextField, {
      props: { label: 'Search', modelValue: '', bare: true },
      attrs: { 'aria-label': 'Search' },
    })
    await nextTick()

    expect(wrapper.find('label').exists()).toBe(false)
    expect(wrapper.get('.field').classes()).toContain('field-bare')
    expect(wrapper.get('input').attributes('aria-label')).toBe('Search')
  })
})
