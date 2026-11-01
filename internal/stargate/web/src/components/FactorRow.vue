<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'

import type { AuthFactor } from '@/lib/api/types'
import { factorTypeDescription, factorTypeLabel } from '@/lib/enums'
import TextField from '@/components/TextField.vue'

const props = defineProps<{
  factor: AuthFactor
  busy: boolean
  codeSent: boolean
  needsCode: boolean
}>()

const emit = defineEmits<{
  sendCode: []
  submit: [secret: string]
}>()

const { t } = useI18n()
const secret = ref('')

function submit() {
  if (!secret.value) return
  emit('submit', secret.value)
  secret.value = ''
}
</script>

<template>
  <div class="rounded-card bg-base-200/40 p-4">
    <div class="flex items-baseline justify-between gap-3">
      <div>
        <label :for="`f-${props.factor.id}`" class="text-xs text-base-content">
          {{ factorTypeLabel(props.factor.type) }}
        </label>
        <p class="mt-0.5 text-2xs text-faint">
          {{ factorTypeDescription(props.factor.type) }}
        </p>
      </div>
      <button
        v-if="props.needsCode"
        type="button"
        class="shrink-0 text-2xs text-dim underline decoration-line underline-offset-4 hover:text-base-content"
        :disabled="props.busy"
        @click="emit('sendCode')"
      >
        {{ props.codeSent ? t('factorRow.resend') : t('factorRow.sendCode') }}
      </button>
    </div>

    <div class="mt-2.5 flex items-start gap-2">
      <TextField
        :id="`f-${props.factor.id}`"
        v-model="secret"
        root-class="min-w-0 flex-1"
        :type="props.needsCode ? 'text' : 'password'"
        :inputmode="props.needsCode ? 'numeric' : undefined"
        :autocomplete="props.needsCode ? 'one-time-code' : 'current-password'"
        :placeholder="props.needsCode ? t('factorRow.codePlaceholder') : undefined"
        :disabled="props.busy"
        @keydown.enter="submit"
      />
      <button
        type="button"
        class="btn btn-primary shrink-0"
        :disabled="props.busy || !secret"
        @click="submit"
      >
        {{ t('common.verify') }}
      </button>
    </div>
  </div>
</template>
