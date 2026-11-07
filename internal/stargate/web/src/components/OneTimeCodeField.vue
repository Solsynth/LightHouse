<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import TextField from '@/components/TextField.vue'

/**
 * A one-time code field: the shape an authenticator app or the operating
 * system recognises for code autofill. A single `<input>` keeps the keyboard
 * suggestion and paste behaviour intact — split digit boxes break both — while
 * the visible caption names the code and the fixed width keeps it legible.
 *
 * Only digits survive input: authenticator and email codes are numeric, and a
 * pasted code usually arrives with spaces or a leftover separator.
 */
const props = withDefaults(
  defineProps<{
    /** Visible caption; also what the browser reads when offering a code. */
    label: string
    /** How many digits the code holds. */
    digits?: number
    disabled?: boolean
    invalid?: boolean
    hint?: string
    placeholder?: string
    size?: 'md' | 'sm' | 'xs'
    /** Focus the control as it appears, so the code suggestion can surface. */
    autofocus?: boolean
    /** Extra classes for the wrapper, for spacing at the call site. */
    rootClass?: string
  }>(),
  { digits: 6, size: 'sm' },
)

const model = defineModel<string>({ default: '' })

const field = ref<InstanceType<typeof TextField> | null>(null)
const maxlength = computed(() => String(props.digits))

function clean(raw: string): string {
  return raw.replace(/\D+/g, '').slice(0, props.digits)
}

/**
 * Sanitise in the same tick as the keystroke or paste. TextField's own handler
 * still commits the value, but writing the corrected string back to the DOM
 * keeps a rejected character from ever being visible.
 */
function onInput(event: Event) {
  const input = event.target as HTMLInputElement
  const next = clean(input.value)
  if (input.value !== next) input.value = next
  if (model.value !== next) model.value = next
}

onMounted(() => {
  if (props.autofocus) field.value?.focus()
})

defineExpose({ focus: () => field.value?.focus() })
</script>

<template>
  <TextField
    ref="field"
    :model-value="model"
    :label="label"
    :disabled="disabled"
    :invalid="invalid"
    :hint="hint"
    :size="size"
    :placeholder="placeholder"
    :root-class="rootClass"
    type="text"
    inputmode="numeric"
    autocomplete="one-time-code"
    name="one-time-code"
    :maxlength="maxlength"
    pattern="[0-9]*"
    enterkeyhint="done"
    autocapitalize="off"
    spellcheck="false"
    control-class="font-mono tracking-[0.18em]"
    @input="onInput"
  />
</template>
