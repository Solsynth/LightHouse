<script setup lang="ts">
import { ref } from 'vue'

import type { AuthFactor } from '@/lib/api/types'
import { factorTypeDescriptions, factorTypeLabels } from '@/lib/enums'

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

const secret = ref('')

function submit() {
  if (!secret.value) return
  emit('submit', secret.value)
  secret.value = ''
}
</script>

<template>
  <div class="border-t border-line-soft py-4 first:border-0 first:pt-0">
    <div class="flex items-baseline justify-between gap-3">
      <div>
        <label :for="`f-${props.factor.id}`" class="text-xs text-base-content">
          {{ factorTypeLabels[props.factor.type] ?? 'factor' }}
        </label>
        <p class="mt-0.5 text-2xs text-faint">
          {{ factorTypeDescriptions[props.factor.type] }}
        </p>
      </div>
      <button
        v-if="props.needsCode"
        type="button"
        class="shrink-0 text-2xs text-dim underline decoration-line underline-offset-4 hover:text-base-content"
        :disabled="props.busy"
        @click="emit('sendCode')"
      >
        {{ props.codeSent ? 'resend' : 'send code' }}
      </button>
    </div>

    <div class="mt-2.5 flex gap-2">
      <input
        :id="`f-${props.factor.id}`"
        v-model="secret"
        class="input input-sm flex-1"
        :type="props.needsCode ? 'text' : 'password'"
        :inputmode="props.needsCode ? 'numeric' : undefined"
        :autocomplete="props.needsCode ? 'one-time-code' : 'current-password'"
        :placeholder="props.needsCode ? '6-digit code' : 'credential'"
        :disabled="props.busy"
        @keydown.enter="submit"
      />
      <button
        type="button"
        class="btn btn-primary btn-sm"
        :disabled="props.busy || !secret"
        @click="submit"
      >
        Verify
      </button>
    </div>
  </div>
</template>
