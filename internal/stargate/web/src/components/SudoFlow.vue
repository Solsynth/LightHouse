<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { FingerprintRecognition16 } from '@carbon/icons-vue'

import { useSessionStore } from '@/stores/session'
import { useElevationStore } from '@/stores/elevation'
import { useNoticeStore } from '@/stores/notice'
import { StargateError, request } from '@/lib/api/client'
import { securityApi, signInApi } from '@/lib/api'
import type { AuthChallenge, AuthFactor } from '@/lib/api/types'
import { codeRequestFactorTypes, factorTypeLabel, pickableFactorTypes } from '@/lib/enums'
import { remaining } from '@/lib/format'
import { getAssertion, passkeyFailure, passkeysSupported } from '@/lib/webauthn'
import TextField from '@/components/TextField.vue'

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
// Passkeys assert through their own endpoint, so the challenge picker never
// lists them; an enabled account factor tells us the ceremony is available.
const passkeySupported = passkeysSupported()
const passkeyFactor = ref(false)

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
  const current = challenge.value
  if (!current) return
  const [list, accountFactors] = await Promise.all([
    request<AuthFactor[]>(`/auth/challenge/${current.id}/factors`),
    securityApi.factors().catch(() => [] as AuthFactor[]),
  ])
  passkeyFactor.value = (accountFactors ?? []).some(
    (factor) => factor.type === 7 && Boolean(factor.enabled_at),
  )
  const blacklist = new Set(current.blacklist_factors)
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

/** Settles the elevation with a registered passkey instead of a typed secret. */
async function submitPasskey() {
  const current = challenge.value
  if (!current) return
  busy.value = true
  error.value = ''
  try {
    const start = await signInApi.passkeyStart(current.id)
    const assertion = await getAssertion({
      challenge: start.challenge,
      rpId: start.rp_id,
      allowCredentials: start.allow_credentials,
      timeout: start.timeout,
      userVerification: start.user_verification,
    })
    const updated = await signInApi.passkeyComplete(current.id, assertion)
    challenge.value = updated
    // The assertion consumed the passkey factor for this challenge.
    passkeyFactor.value = false
    if (updated.step_remain === 0) {
      session.noteElevation(updated)
      notice.ok(t('elevation.noticeGranted'))
      emit('done', updated)
      reset()
    } else {
      await loadFactors()
    }
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : passkeyFailure(caught)
  } finally {
    busy.value = false
  }
}

function reset() {
  factors.value = []
  used.value = []
  passkeyFactor.value = false
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
        {{
          t('elevation.steps', {
            done: challenge.step_total - challenge.step_remain,
            total: challenge.step_total,
          })
        }}
      </span>
      <span>{{ t('elevation.timeLeft', { time: remaining(challenge.expired_at) }) }}</span>
    </div>

    <p v-if="busy && !factors.length && !passkeyFactor" class="py-6 text-xs text-faint">
      {{ t('elevation.starting') }}
    </p>

    <p v-else-if="!factors.length && !passkeyFactor" class="voice py-6">
      {{ t('elevation.empty') }}
    </p>

    <button
      v-if="passkeySupported && passkeyFactor"
      type="button"
      class="btn btn-outline mb-4 w-full"
      :disabled="busy"
      @click="submitPasskey"
    >
      <FingerprintRecognition16 aria-hidden="true" />
      {{ t('elevation.usePasskey') }}
    </button>

    <ul v-if="factors.length" class="space-y-5">
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
        <div class="mt-2 flex items-start gap-2">
          <TextField
            :id="`factor-${factor.id}`"
            v-model="secrets[factor.id]"
            root-class="min-w-0 flex-1"
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
            class="btn btn-primary btn-field shrink-0"
            :disabled="!secrets[factor.id] || busy"
            @click="submit(factor)"
          >
            {{ t('common.verify') }}
          </button>
        </div>
      </li>
    </ul>

    <p v-if="error" class="note note-error mt-4">{{ error }}</p>
  </div>
</template>
