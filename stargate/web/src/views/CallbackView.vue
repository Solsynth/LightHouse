<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'

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
    <h1 class="title mt-2 text-2xl text-base-content">Continuing Sign-In</h1>
    <p v-if="!failed" class="voice mt-2">
      Handing this request to the identity service. If nothing happens, continue manually.
    </p>
    <p v-else class="voice mt-2">
      This callback is missing a provider. Return to sign-in and choose a provider again.
    </p>

    <a v-if="!failed" :href="target" class="link-quiet mt-6 inline-block text-xs">
      Continue to the Identity Service
    </a>
    <RouterLink v-else to="/sign-in" class="link-quiet mt-6 inline-block text-xs">
      Back to Sign-In
    </RouterLink>
  </div>
</template>
