<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import type { AuthFactor } from '@/lib/api/types'
import { factorTypeLabel } from '@/lib/enums'
import OneTimeCodeField from '@/components/OneTimeCodeField.vue'
import TextField from '@/components/TextField.vue'

/**
 * The secret a factor asks for, in the form that factor needs:
 *
 * - a timed or emailed code is a one-time code, so browsers and password
 *   managers may autofill it (`autocomplete="one-time-code"`),
 * - a password stays a password field, and a PIN hides its digits,
 * - a recovery code is typed or pasted in full.
 *
 * The caption names the credential itself. A bare field beside a factor title
 * gives autofill nothing to match, and gives the control no accessible name of
 * its own.
 */
const props = defineProps<{
  factor: AuthFactor
  id?: string
  disabled?: boolean
  invalid?: boolean
  hint?: string
  autofocus?: boolean
  /** Extra classes for the wrapper, for spacing at the call site. */
  rootClass?: string
}>()

const model = defineModel<string>({ default: '' })
const { t } = useI18n()

type SecretKind = 'password' | 'email' | 'totp' | 'pin' | 'recovery' | 'nfc'

const kind = computed<SecretKind>(() => {
  switch (props.factor.type) {
    case 1:
      return 'email'
    case 3:
      return 'totp'
    case 4:
      return 'pin'
    case 5:
      return 'recovery'
    case 6:
      return 'nfc'
    default:
      return 'password'
  }
})

/** Timed and emailed codes are numeric and recognised by the browser. */
const oneTime = computed(() => kind.value === 'email' || kind.value === 'totp')

const label = computed(() =>
  t(`factorSecret.${kind.value}`, { factor: factorTypeLabel(props.factor.type) }),
)

const placeholder = computed(() => {
  if (oneTime.value) return t('factorSecret.codePlaceholder')
  if (kind.value === 'recovery') return t('factorSecret.recoveryPlaceholder')
  return undefined
})

const type = computed(() =>
  kind.value === 'password' || kind.value === 'pin' ? 'password' : 'text',
)

const autocomplete = computed(() => {
  if (kind.value === 'password') return 'current-password'
  return 'off'
})

const inputmode = computed(() => (kind.value === 'pin' ? 'numeric' : undefined))
const field = ref<InstanceType<typeof TextField> | InstanceType<typeof OneTimeCodeField> | null>(
  null,
)

onMounted(() => {
  if (props.autofocus) field.value?.focus()
})
</script>

<template>
  <OneTimeCodeField
    v-if="oneTime"
    :id="id"
    ref="field"
    v-model="model"
    :label="label"
    :placeholder="placeholder"
    :disabled="disabled"
    :invalid="invalid"
    :hint="hint"
    :root-class="rootClass"
    :autofocus="autofocus"
  />
  <TextField
    v-else
    :id="id"
    ref="field"
    v-model="model"
    :label="label"
    :placeholder="placeholder"
    :type="type"
    :inputmode="inputmode"
    :autocomplete="autocomplete"
    :disabled="disabled"
    :invalid="invalid"
    :hint="hint"
    :root-class="rootClass"
    control-class="font-mono"
  />
</template>
