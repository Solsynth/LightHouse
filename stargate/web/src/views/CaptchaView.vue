<script setup lang="ts">
import { ref } from 'vue'
import { useRoute } from 'vue-router'

import CaptchaWidget from '@/components/CaptchaWidget.vue'

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
      error.value = 'The redirect address is invalid.'
    }
  }
  if (!redirectUri && window.parent === window) {
    verified.value = true
  }
}
</script>

<template>
  <div>
    <h1 class="title mt-2 text-2xl text-base-content">Verify You Are Human</h1>
    <p class="voice mt-2">
      Complete the challenge to continue. The result is returned to the page that asked for it.
    </p>

    <div class="mt-6 flex justify-center border border-line bg-base-200/40 px-4 py-5">
      <CaptchaWidget @verified="accept" />
    </div>

    <p v-if="verified" class="mt-4 border-l border-success pl-3 text-xs text-success">
      Verification complete. Return to the form that asked for it.
    </p>
    <p v-if="!redirectUri" class="voice mt-4 text-[12px]">
      This page returns the token to the page that opened it. Open it from the form that needs
      verification to continue automatically.
    </p>
    <p v-if="error" class="mt-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>
  </div>
</template>
