<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'

const { t } = useI18n()
const route = useRoute()
const failed = ref(false)

const provider = typeof route.params.provider === 'string' ? route.params.provider : ''
const query = new URLSearchParams()
for (const [key, value] of Object.entries(route.query)) {
  if (typeof value === 'string') query.set(key, value)
  else if (Array.isArray(value)) value.forEach((entry) => entry !== null && query.append(key, entry))
}
const suffix = query.toString()
const target = `/api/auth/callback/${encodeURIComponent(provider)}${suffix ? `?${suffix}` : ''}`

onMounted(() => {
  if (!provider) {
    failed.value = true
    return
  }
  // The API owns the provider exchange, the session cookies, and the final
  // redirect. This page exists so the provider can return to the site origin.
  window.location.replace(target)
})
</script>

<template>
  <div>
    <h1 class="title mt-2 text-2xl text-base-content">{{ t('callback.title') }}</h1>
    <p v-if="!failed" class="voice mt-2">
      {{ t('callback.handingOff') }}
    </p>
    <p v-else class="voice mt-2">
      {{ t('callback.missingProvider') }}
    </p>

    <a v-if="!failed" :href="target" class="link-quiet mt-6 inline-block text-xs">
      {{ t('callback.continueManual') }}
    </a>
    <RouterLink v-else to="/sign-in" class="link-quiet mt-6 inline-block text-xs">
      {{ t('callback.backToSignIn') }}
    </RouterLink>
  </div>
</template>
