<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Launch16 } from '@carbon/icons-vue'
import { useRoute, useRouter } from 'vue-router'

import { useSessionStore } from '@/stores/session'
import { authApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AuthorizeClientInfo } from '@/lib/api/types'

const route = useRoute()
const router = useRouter()
const session = useSessionStore()

const info = ref<AuthorizeClientInfo | null>(null)
const loading = ref(true)
const busy = ref(false)
const error = ref('')

const scopeLabels: Record<string, string> = {
  openid: 'Read your Solar Network profile',
  profile: 'Read your public profile information',
  email: 'Read your email address',
  offline_access: 'Stay connected when you are not using the app',
  '*': 'Full access — this app can do anything as you',
}

const name = computed(() => info.value?.client_name || info.value?.client_id || 'This App')

function queryParams(): URLSearchParams {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(route.query)) {
    if (typeof value === 'string') params.set(key, value)
    else if (Array.isArray(value)) value.forEach((entry) => entry !== null && params.append(key, entry))
  }
  return params
}

async function load() {
  loading.value = true
  error.value = ''
  const params: Record<string, string> = {}
  queryParams().forEach((value, key) => {
    params[key] = value
  })
  try {
    info.value = await authApi.authorizeInfo(params)
  } catch (caught) {
    error.value =
      caught instanceof StargateError
        ? caught.message
        : 'The authorization request could not be read.'
  } finally {
    loading.value = false
  }
}

async function decide(approve: boolean) {
  busy.value = true
  error.value = ''
  try {
    const form = queryParams()
    form.set('authorize', approve ? 'true' : 'false')
    const reply = await authApi.authorizeDecision(form)
    if (reply.redirect_uri) {
      window.location.assign(reply.redirect_uri)
      return
    }
    await router.push('/account')
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : 'The decision could not be submitted.'
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <h1 class="title mt-2 text-2xl text-base-content">
      {{ loading ? 'Authorization request' : `Authorize ${name}` }}
    </h1>
    <p class="voice mt-2">
      This application asks to act with your account
      <span class="text-base-content">{{ session.identity?.name }}</span
      >. You will return to the application after deciding.
    </p>

    <p v-if="loading" class="mt-6 text-xs text-faint">Reading the request…</p>

    <template v-else-if="info">
      <div class="mt-6 border border-line">
        <p class="eyebrow border-b border-line px-4 py-2.5">requested access</p>
        <ul class="divide-y divide-line-soft">
          <li v-for="scope in info.scopes" :key="scope" class="flex items-baseline gap-3 px-4 py-2.5">
            <span class="text-2xs text-faint">{{ scope }}</span>
            <span class="text-xs text-base-content">{{ scopeLabels[scope] ?? scope }}</span>
          </li>
        </ul>
      </div>

      <p v-if="info.home_uri" class="voice mt-3 text-[12px]">
        <a
          :href="info.home_uri"
          class="link-quiet inline-flex items-center gap-1.5"
          target="_blank"
          rel="noreferrer"
        >
          <Launch16 aria-hidden="true" />
          {{ info.home_uri }}
        </a>
      </p>

      <div class="mt-6 flex flex-wrap gap-2">
        <button
          type="button"
          class="btn btn-primary btn-sm"
          :disabled="busy"
          @click="decide(true)"
        >
          {{ busy ? 'Submitting…' : 'Approve' }}
        </button>
        <button type="button" class="btn btn-ghost btn-sm" :disabled="busy" @click="decide(false)">
          Deny
        </button>
      </div>
    </template>

    <p v-if="error" class="mt-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>
  </div>
</template>
