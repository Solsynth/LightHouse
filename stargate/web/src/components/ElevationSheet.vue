<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { useSessionStore } from '@/stores/session'
import { useElevationStore } from '@/stores/elevation'
import { useNoticeStore } from '@/stores/notice'
import { request } from '@/lib/api/client'
import type { AuthChallenge, AuthFactor } from '@/lib/api/types'
import { codeRequestFactorTypes, factorTypeLabel, pickableFactorTypes } from '@/lib/enums'
import { remaining } from '@/lib/format'

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

function dismiss() {
  session.dismissElevation()
  elevation.reset()
  reset()
}

function onKey(event: KeyboardEvent) {
  if (event.key === 'Escape' && session.elevationOpen) dismiss()
}

watch(
  () => session.elevationOpen,
  (open) => {
    if (open && !challenge.value) void begin()
  },
  { immediate: true },
)

onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <dialog
    v-if="session.elevationOpen"
    class="modal modal-open"
    aria-labelledby="elevation-title"
    aria-modal="true"
  >
    <div class="modal-box max-w-md rounded-[2px] border border-line bg-base-100 p-0 shadow-none">
      <header class="border-b border-line px-5 py-4">
        <p class="eyebrow">{{ t('elevation.required') }}</p>
        <h2 id="elevation-title" class="mt-1 text-sm text-base-content">{{ t('elevation.title') }}</h2>
        <p class="voice mt-2">
          {{ t('elevation.description') }}
        </p>
      </header>

      <div class="px-5 py-4">
        <div v-if="challenge" class="mb-4 flex items-center justify-between text-2xs text-faint">
          <span>
            {{ t('elevation.steps', {
              done: challenge.step_total - challenge.step_remain,
              total: challenge.step_total,
            }) }}
          </span>
          <span>{{ t('elevation.timeLeft', { time: remaining(challenge.expired_at) }) }}</span>
        </div>

        <p v-if="busy && !factors.length" class="py-6 text-xs text-faint">{{ t('elevation.starting') }}</p>

        <p v-else-if="!factors.length" class="voice py-6">
          {{ t('elevation.empty') }}
        </p>

        <ul v-else class="space-y-4">
          <li
            v-for="factor in factors"
            :key="factor.id"
            class="border-t border-line-soft pt-4 first:border-0 first:pt-0"
          >
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

      <footer class="flex items-center justify-between border-t border-line px-5 py-3">
        <span class="text-2xs text-faint">{{ t('elevation.footer') }}</span>
        <button type="button" class="btn btn-ghost btn-xs" @click="dismiss">{{ t('common.cancel') }}</button>
      </footer>
    </div>
    <button
      type="button"
      class="modal-backdrop bg-black/60"
      :aria-label="t('elevation.cancelElevation')"
      @click="dismiss"
    />
  </dialog>
</template>
