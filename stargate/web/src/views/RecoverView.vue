<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { landingQuery } from '@/lib/signInTarget'
import { authApi, signInApi } from '@/lib/api'
import CaptchaWidget from '@/components/CaptchaWidget.vue'
import type { CaptchaConfig } from '@/lib/api/types'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const notice = useNoticeStore()

const captcha = ref<CaptchaConfig | null>(null)
const account = ref('')
const captchaToken = ref('')
const busy = ref(false)
const error = ref('')
const sent = ref(false)


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
    notice.ok(t('recover.noticeRequested'))
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : t('recover.errorRequestFailed')
  } finally {
    busy.value = false
  }
}

onMounted(async () => {
  // The deployment's standalone verification page hands its token back through
  // the address. It is single-use at the provider, so it goes straight into the
  // reset request — that request is the check.
  if (typeof route.query.captcha_tk === 'string') {
    captchaToken.value = route.query.captcha_tk
    const rest = { ...route.query }
    delete rest.captcha_tk
    await router.replace({ name: 'recover', query: rest })
  }
  try {
    captcha.value = await authApi.captchaConfig()
  } catch {
    captcha.value = null
  }
})
</script>

<template>
  <div>
    <div class="mb-8">
      <h1 class="title mt-2 text-2xl text-base-content">{{ t('recover.title') }}</h1>
      <p class="voice mt-2">
        {{ t('recover.intro') }}
      </p>
    </div>

    <form class="border border-line bg-base-200/40 p-5" @submit.prevent="requestReset">
      <label for="recover-account" class="field-label">{{ t('signIn.accountLabel') }}</label>
      <input
        id="recover-account"
        v-model="account"
        class="input"
        autocomplete="username"
        autocapitalize="off"
        spellcheck="false"
        :placeholder="t('signIn.accountPlaceholder')"
      />

      <div v-if="captcha" class="mt-4">
        <p class="field-label">{{ t('recover.verification') }}</p>
        <CaptchaWidget
          :provider="captcha.provider"
          :api-key="captcha.apiKey"
          @verified="captchaToken = $event"
        />
      </div>

      <p v-if="error" class="mt-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>
      <p v-if="sent" class="mt-4 border-l border-success pl-3 text-xs text-success">
        {{ t('recover.accepted') }}
      </p>

      <button
        type="submit"
        class="btn btn-primary mt-5 w-full"
        :disabled="busy || !account.trim()"
      >
        {{ busy ? t('recover.requesting') : t('recover.submit') }}
      </button>
    </form>

    <p class="voice mt-6 text-[12px]">
      <RouterLink :to="{ name: 'sign-in', query: landingQuery(route.query) }" class="link-quiet">{{
        t('recover.backToSignIn')
      }}</RouterLink
      >
    </p>
  </div>
</template>
