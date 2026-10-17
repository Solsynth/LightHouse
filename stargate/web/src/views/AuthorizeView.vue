<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Launch16 } from '@carbon/icons-vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'

import { useSessionStore } from '@/stores/session'
import { authApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AuthorizeClientInfo } from '@/lib/api/types'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const session = useSessionStore()

const info = ref<AuthorizeClientInfo | null>(null)
const loading = ref(true)
const busy = ref(false)
const error = ref('')

const scopeLabels = computed<Record<string, string>>(() => ({
  openid: t('authorize.scopeOpenid'),
  profile: t('authorize.scopeProfile'),
  email: t('authorize.scopeEmail'),
  offline_access: t('authorize.scopeOfflineAccess'),
  '*': t('authorize.scopeAll'),
}))

const name = computed(() => info.value?.client_name || info.value?.client_id || t('authorize.thisApp'))

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
        : t('authorize.errorRead')
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
      caught instanceof StargateError ? caught.message : t('authorize.errorSubmit')
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <h1 class="title mt-2 text-2xl text-base-content">
      {{ loading ? t('authorize.loadingTitle') : t('authorize.title', { name }) }}
    </h1>
    <p class="voice mt-2">
      {{ t('authorize.introPrefix') }}
      <span class="text-base-content">{{ session.identity?.name }}</span
      >{{ t('authorize.introSuffix') }}
    </p>

    <div v-if="loading" class="mt-6 border border-line" aria-hidden="true">
      <span class="skeleton mx-4 my-2.5 h-2 w-24" />
      <div class="space-y-3 border-t border-line px-4 py-3">
        <span class="skeleton h-3 w-2/3" />
        <span class="skeleton h-3 w-1/2" />
      </div>
    </div>

    <template v-else-if="info">
      <div class="mt-6 border border-line">
        <p class="eyebrow px-4 py-2.5">{{ t('authorize.requestedAccess') }}</p>
        <ul>
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
          {{ busy ? t('authorize.submitting') : t('authorize.approve') }}
        </button>
        <button type="button" class="btn btn-ghost btn-sm" :disabled="busy" @click="decide(false)">
          {{ t('authorize.deny') }}
        </button>
      </div>
    </template>

    <p v-if="error" class="mt-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>
  </div>
</template>
