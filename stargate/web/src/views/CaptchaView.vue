<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'

import CaptchaWidget from '@/components/CaptchaWidget.vue'

const { t } = useI18n()
const route = useRoute()
const error = ref('')
const verified = ref(false)

const redirectUri = typeof route.query.redirect_uri === 'string' ? route.query.redirect_uri : ''

function accept(token: string) {
  const value = token.trim()
  if (!value) return

  // Other origins embed this page in an iframe and read the token from the
  // message channel; direct visitors continue to the redirect target instead.
  if (window.parent !== window) {
    window.parent.postMessage(`captcha_tk=${value}`, '*')
  }

  if (redirectUri) {
    try {
      const url = new URL(redirectUri)
      url.searchParams.set('captcha_tk', value)
      window.location.assign(url.toString())
    } catch {
      error.value = t('captcha.errorInvalidRedirect')
    }
  }
  if (!redirectUri && window.parent === window) {
    verified.value = true
  }
}
</script>

<template>
  <div>
    <h1 class="title mt-2 text-2xl text-base-content">{{ t('captcha.title') }}</h1>
    <p class="voice mt-2">
      {{ t('captcha.intro') }}
    </p>

    <div class="mt-6 flex justify-center border border-line bg-base-200/40 px-4 py-5">
      <CaptchaWidget @verified="accept" />
    </div>

    <p v-if="verified" class="mt-4 border-l border-success pl-3 text-xs text-success">
      {{ t('captcha.verified') }}
    </p>
    <p v-if="!redirectUri" class="voice mt-4 text-[12px]">
      {{ t('captcha.tokenNote') }}
    </p>
    <p v-if="error" class="mt-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>
  </div>
</template>
