<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { useSessionStore } from '@/stores/session'
import { useElevationStore } from '@/stores/elevation'
import { useNoticeStore } from '@/stores/notice'
import { request } from '@/lib/api/client'
import type { AuthChallenge, AuthFactor } from '@/lib/api/types'
import { codeRequestFactorTypes, factorTypeLabel, pickableFactorTypes } from '@/lib/enums'
import { remaining } from '@/lib/format'

const emit = defineEmits<{ done: [challenge: AuthChallenge] }>()

const { t } = useI18n()
const session = useSessionStore()
const elevation = useElevationStore()
const notice = useNoticeStore()

const factors = ref<AuthFactor[]>([])
const used = ref<string[]>([])
const secrets = ref<Record<string, string>>({})
const codeSentFor = ref<string | null>(null)
const error = ref('')
const busy = ref(false)
const challenge = ref<AuthChallenge | null>(null)

async function begin() {
  busy.value = true
  error.value = ''
  try {
    challenge.value = elevation.state?.challenge ?? (await elevation.begin())
    await loadFactors()
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : t('elevation.errorStart')
  } finally {
    busy.value = false
  }
}

async function loadFactors() {
  if (!challenge.value) return
  const list = await request<AuthFactor[]>(`/auth/challenge/${challenge.value.id}/factors`)
  const blacklist = new Set(challenge.value.blacklist_factors)
  factors.value = list.filter(
    (factor) =>
      pickableFactorTypes[factor.type] === true &&
      !blacklist.has(factor.id) &&
      !used.value.includes(factor.id),
  )
}

async function sendCode(factor: AuthFactor) {
  if (!challenge.value) return
  error.value = ''
  try {
    await request<void>(`/auth/challenge/${challenge.value.id}/factors/${factor.id}`, {
      method: 'POST',
    })
    codeSentFor.value = factor.id
    notice.ok(t('elevation.noticeCodeSent', { factor: factorTypeLabel(factor.type) }))
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : t('elevation.errorCode')
  }
}

async function submit(factor: AuthFactor) {
  const secret = secrets.value[factor.id]
  if (!challenge.value || !secret) return
  busy.value = true
  error.value = ''
  try {
    const updated = await request<AuthChallenge>(`/auth/challenge/${challenge.value.id}`, {
      method: 'PATCH',
      body: { factor_id: factor.id, password: secret },
    })
    challenge.value = updated
    used.value = [...used.value, factor.id]
    secrets.value = {}
    codeSentFor.value = null
    if (updated.step_remain === 0) {
      session.noteElevation(updated)
      notice.ok(t('elevation.noticeGranted'))
      emit('done', updated)
      reset()
    } else {
      await loadFactors()
    }
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : t('errors.credentialRejected')
  } finally {
    busy.value = false
  }
}

function reset() {
  factors.value = []
  used.value = []
  secrets.value = {}
  codeSentFor.value = null
  error.value = ''
  challenge.value = null
}

onMounted(begin)
</script>

<template>
  <div>
    <div v-if="challenge" class="mb-4 flex items-center justify-between text-2xs text-faint">
      <span>
        {{ t('elevation.steps', {
          done: challenge.step_total - challenge.step_remain,
          total: challenge.step_total,
        }) }}
      </span>
      <span>{{ t('elevation.timeLeft', { time: remaining(challenge.expired_at) }) }}</span>
    </div>

    <p v-if="busy && !factors.length" class="py-6 text-xs text-faint">
      {{ t('elevation.starting') }}
    </p>

    <p v-else-if="!factors.length" class="voice py-6">
      {{ t('elevation.empty') }}
    </p>

    <ul v-else class="space-y-5">
      <li v-for="factor in factors" :key="factor.id">
        <div class="flex items-baseline justify-between gap-3">
          <label :for="`factor-${factor.id}`" class="text-xs text-base-content">
            {{ factorTypeLabel(factor.type) }}
          </label>
          <button
            v-if="codeRequestFactorTypes[factor.type] === true"
            type="button"
            class="text-2xs text-dim underline decoration-line underline-offset-4 hover:text-base-content"
            @click="sendCode(factor)"
          >
            {{ codeSentFor === factor.id ? t('elevation.resendCode') : t('elevation.sendCode') }}
          </button>
        </div>
        <div class="mt-2 flex gap-2">
          <input
            :id="`factor-${factor.id}`"
            v-model="secrets[factor.id]"
            class="input input-sm flex-1"
            type="password"
            autocomplete="current-password"
            :placeholder="
              codeRequestFactorTypes[factor.type] === true
                ? t('elevation.codePlaceholder')
                : t('elevation.credentialPlaceholder')
            "
            @keydown.enter="submit(factor)"
          />
          <button
            type="button"
            class="btn btn-primary btn-sm"
            :disabled="!secrets[factor.id] || busy"
            @click="submit(factor)"
          >
            {{ t('common.verify') }}
          </button>
        </div>
      </li>
    </ul>

    <p v-if="error" class="mt-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>
  </div>
</template>
