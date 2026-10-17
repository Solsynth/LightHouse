<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { authApi, signInApi } from '@/lib/api'
import CaptchaWidget from '@/components/CaptchaWidget.vue'
import type { CaptchaConfig } from '@/lib/api/types'

const route = useRoute()
const notice = useNoticeStore()

const captcha = ref<CaptchaConfig | null>(null)
const account = ref('')
const captchaToken = ref('')
const busy = ref(false)
const error = ref('')
const sent = ref(false)
const resetting = ref(false)

async function requestReset() {
  if (!account.value.trim()) return
  busy.value = true
  error.value = ''
  try {
    await signInApi.requestPasswordReset({
      account: account.value.trim(),
      captcha_token: captchaToken.value,
    })
    sent.value = true
    notice.ok('If the account has a verified contact, a reset link is on its way.')
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : 'The reset request failed.'
  } finally {
    busy.value = false
  }
}

/** A reset link lands here with ?word=…; applying it verifies the contact. */
async function applySpell() {
  const word = typeof route.query.word === 'string' ? route.query.word : ''
  if (!word) return
  resetting.value = true
  try {
    await signInApi.applySpell(word)
    notice.ok('Link applied. You can sign in with your new password.')
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : 'The link could not be applied.'
  } finally {
    resetting.value = false
  }
}

onMounted(async () => {
  try {
    captcha.value = await authApi.captchaConfig()
  } catch {
    captcha.value = null
  }
  await applySpell()
})
</script>

<template>
  <div>
    <div class="mb-8">
      <h1 class="title mt-2 text-2xl text-base-content">Reset a Password</h1>
      <p class="voice mt-2">
        We email a reset link to the account's verified contact. If the account has none, no link
        is sent.
      </p>
    </div>

    <div v-if="resetting" class="border border-line bg-base-200/40 p-5 text-xs text-dim">
      Applying reset link…
    </div>

    <form v-else class="border border-line bg-base-200/40 p-5" @submit.prevent="requestReset">
      <label for="recover-account" class="field-label">Account or email</label>
      <input
        id="recover-account"
        v-model="account"
        class="input"
        autocomplete="username"
        autocapitalize="off"
        spellcheck="false"
        placeholder="name or name@example.com"
      />

      <div v-if="captcha" class="mt-4">
        <p class="field-label">Verification</p>
        <CaptchaWidget
          :provider="captcha.provider"
          :api-key="captcha.apiKey"
          @verified="captchaToken = $event"
        />
      </div>

      <p v-if="error" class="mt-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>
      <p v-if="sent" class="mt-4 border-l border-success pl-3 text-xs text-success">
        Request accepted. Check the inbox for the reset link.
      </p>

      <button
        type="submit"
        class="btn btn-primary mt-5 w-full"
        :disabled="busy || !account.trim()"
      >
        {{ busy ? 'Requesting…' : 'Send Reset Link' }}
      </button>
    </form>

    <p class="voice mt-6 text-[12px]">
      <RouterLink to="/sign-in" class="link-quiet">Back to Sign In</RouterLink>
    </p>
  </div>
</template>
