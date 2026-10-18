<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { signInApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { MagicSpell } from '@/lib/api/types'

/**
 * The page the emailed magic-spell links land on. Stargate mints
 * `{siteUrl}/spells/{word}` for verification and password-reset emails, so this
 * route is the only thing standing between the recipient and a 404.
 *
 * The word travels in the address and is the credential: it is never rendered,
 * never stored, and only ever handed back to the API that issued it. The type
 * decides the form — a reset spell installs a new password, a verification
 * spell is confirmed with a single action.
 */
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const notice = useNoticeStore()

/** MagicSpellType ordinals. Only these two are redeemable from an emailed link. */
const RESET = 3
const CONTACT_VERIFICATION = 4

const word = typeof route.params.word === 'string' ? route.params.word : ''

const loading = ref(true)
const spell = ref<MagicSpell | null>(null)
const state = ref<'ready' | 'expired' | 'missing' | 'failed' | 'unsupported'>('ready')
const password = ref('')
const confirm = ref('')
const busy = ref(false)
const done = ref(false)
const error = ref('')

const isReset = computed(() => spell.value?.type === RESET)

/**
 * A spell whose type this page cannot redeem — an activation or removal spell
 * minted by another client — must not render a form whose submit can only
 * fail, so it is reported as an unsupported link instead.
 */
const redeemable = computed(() =>
  spell.value?.type === RESET ? true : spell.value?.type === CONTACT_VERIFICATION,
)

const recipient = computed(() => {
  const account = spell.value?.account
  if (!account) return ''
  return account.nick?.trim() || account.name || ''
})

/** Mirrors the server's own rule (4–128); the API is the check that counts. */
const passwordProblem = computed(() => {
  const value = password.value
  if (!value) return ''
  if (value.length < 4) return t('createAccount.passwordTooShort')
  if (value.length > 128) return t('createAccount.passwordTooLong')
  if (confirm.value && confirm.value !== value) return t('spell.passwordMismatch')
  return ''
})

const canSubmit = computed(() =>
  isReset.value
    ? password.value.length >= 4 &&
      password.value.length <= 128 &&
      confirm.value === password.value &&
      !busy.value
    : !busy.value,
)

/**
 * The heading, lead, and detail line are resolved from the state rather than
 * branched across the template: every state needs all three, and keeping them
 * together makes it obvious that no state renders half a sentence.
 */
const heading = computed(() => {
  if (state.value === 'expired') return t('spell.expiredIntro')
  if (state.value === 'missing') return t('spell.missingIntro')
  if (state.value === 'failed') return t('spell.failedIntro')
  if (state.value === 'unsupported') return t('spell.unsupportedIntro')
  return isReset.value ? t('spell.resetTitle') : t('spell.verifyTitle')
})

const intro = computed(() => {
  if (state.value === 'expired') return t('spell.expiredBody')
  if (state.value === 'missing') return t('spell.missingBody')
  if (state.value === 'failed') return t('spell.failedBody')
  if (state.value === 'unsupported') return t('spell.unsupportedBody')
  if (done.value) return t('spell.doneIntro')
  return isReset.value ? t('spell.resetIntro') : t('spell.verifyIntro')
})

/**
 * Only an expired link has something the reader can do about it; a mistyped,
 * already-used, or unsupported word has no action beyond going back to sign-in,
 * so the header's sentence is the whole message there.
 */
const canRequestNew = computed(() => state.value === 'expired')

async function load() {
  if (!word) {
    state.value = 'missing'
    loading.value = false
    return
  }
  try {
    spell.value = await signInApi.getSpell(word)
    state.value = redeemable.value ? 'ready' : 'unsupported'
  } catch (caught) {
    if (caught instanceof StargateError && caught.status === 410) state.value = 'expired'
    else if (caught instanceof StargateError && caught.status === 404) state.value = 'missing'
    else state.value = 'failed'
  } finally {
    loading.value = false
  }
}

async function apply() {
  if (!canSubmit.value) return
  busy.value = true
  error.value = ''
  try {
    // A reset spell consumes the word and installs the password in one request:
    // the word is single-use, so validating it in a separate call first would
    // burn it before the new password ever reached the server.
    await signInApi.applySpell(word, isReset.value ? password.value : undefined)
    done.value = true
    password.value = ''
    confirm.value = ''
    notice.ok(isReset.value ? t('spell.noticeReset') : t('spell.noticeVerified'))
    await router.replace({ name: 'sign-in' })
  } catch (caught) {
    if (caught instanceof StargateError && caught.status === 410) {
      state.value = 'expired'
    } else {
      error.value = caught instanceof Error ? caught.message : t('spell.errorApplyFailed')
    }
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="mb-8">
      <h1 class="title mt-2 text-2xl text-base-content">{{ heading }}</h1>
      <p class="voice mt-2">{{ intro }}</p>
    </div>

    <p v-if="loading" class="flex items-center gap-2 text-2xs text-faint">
      <span class="dot animate-pulse bg-info" aria-hidden="true" />
      {{ t('spell.loading') }}
    </p>

    <!-- Only an expired link has an action left. Every other dead end is fully
         described by the header, so a panel there would just repeat it. -->
    <div v-else-if="canRequestNew" class="border border-warning/40 bg-base-200/40 p-5">
      <RouterLink to="/recover" class="btn btn-primary btn-sm">
        {{ t('spell.requestNew') }}
      </RouterLink>
    </div>


    <!-- The form only exists for a live spell: rendering it for a dead word
         would offer a submit that can only fail. -->
    <form
      v-else-if="state === 'ready' && !done"
      class="border border-line bg-base-200/40 p-5"
      @submit.prevent="apply"
    >
      <p v-if="recipient" class="voice mb-4 text-[12px]">
        {{ t('spell.forAccount', { name: recipient }) }}
      </p>

      <div v-if="isReset">
        <label for="spell-password" class="field-label">{{ t('spell.newPassword') }}</label>
        <input
          id="spell-password"
          v-model="password"
          class="input"
          type="password"
          autocomplete="new-password"
        />
      </div>

      <div v-if="isReset" class="mt-4">
        <label for="spell-confirm" class="field-label">{{ t('spell.confirmPassword') }}</label>
        <input
          id="spell-confirm"
          v-model="confirm"
          class="input"
          type="password"
          autocomplete="new-password"
        />
        <p v-if="passwordProblem" class="mt-1 text-2xs text-error">{{ passwordProblem }}</p>
      </div>

      <button type="submit" class="btn btn-primary mt-5 w-full" :disabled="!canSubmit">
        {{ busy ? t('spell.applying') : isReset ? t('spell.submitReset') : t('spell.submitVerify') }}
      </button>

      <p v-if="error" class="mt-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>
    </form>

    <p v-if="done" class="border-l border-success pl-3 text-xs text-success">
      {{ isReset ? t('spell.noticeReset') : t('spell.noticeVerified') }}
    </p>

    <p class="voice mt-6 text-[12px]">
      <RouterLink to="/sign-in" class="link-quiet">{{ t('spell.backToSignIn') }}</RouterLink>
    </p>
  </div>
</template>
