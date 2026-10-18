<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Launch16 } from '@carbon/icons-vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'

import { useSessionStore } from '@/stores/session'
import { useUiStore } from '@/stores/ui'
import { authApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AuthorizeClientInfo, CloudFileRef } from '@/lib/api/types'
import AppMark from '@/components/AppMark.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const session = useSessionStore()
const ui = useUiStore()

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

/**
 * The requesting app's own background takes over the shell's backdrop while
 * this page is open, so the consent screen carries the app's identity instead
 * of the account's. The shell falls back to the account background when the
 * app declares none.
 */
const backdrop = computed<CloudFileRef | null>(() => info.value?.background ?? null)

function publishBackdrop() {
  ui.setAppBackdrop(backdrop.value?.url ?? '')
}

watch(backdrop, publishBackdrop)
onMounted(publishBackdrop)
onBeforeUnmount(() => ui.setAppBackdrop(''))

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
      <!-- The app introduces itself in the rail; the scopes it wants and the
           decision sit opposite it, so the request reads as a request. -->
      <div class="mt-6 border border-line sm:grid sm:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]">
        <section class="border-b border-line bg-base-200/40 p-4 sm:border-b-0 sm:border-r">
          <div class="flex items-start gap-3">
            <AppMark :picture="info.picture" :name="name" :size="40" />
            <div class="min-w-0">
              <p class="text-[15px] text-base-content">{{ name }}</p>
              <p class="text-2xs text-faint">{{ t('authorize.requestsAccess') }}</p>
            </div>
          </div>

          <p class="mt-4 text-2xs text-dim" :title="info.client_id">{{ info.client_id }}</p>

          <div v-if="info.policy_uri || info.terms_of_service_uri" class="mt-4 flex flex-wrap gap-3">
            <a
              v-if="info.policy_uri"
              :href="info.policy_uri"
              class="link-quiet inline-flex items-center gap-1.5 text-[12px]"
              target="_blank"
              rel="noreferrer"
            >
              <Launch16 aria-hidden="true" />
              {{ t('authorize.privacyPolicy') }}
            </a>
            <a
              v-if="info.terms_of_service_uri"
              :href="info.terms_of_service_uri"
              class="link-quiet inline-flex items-center gap-1.5 text-[12px]"
              target="_blank"
              rel="noreferrer"
            >
              <Launch16 aria-hidden="true" />
              {{ t('authorize.termsOfService') }}
            </a>
          </div>
        </section>

        <section class="p-4">
          <p class="eyebrow">{{ t('authorize.requestedAccess') }}</p>
          <ul class="mt-2">
            <li v-for="scope in info.scopes" :key="scope" class="flex items-baseline gap-3 py-2">
              <span class="text-2xs text-faint">{{ scope }}</span>
              <span class="text-xs text-base-content">{{ scopeLabels[scope] ?? scope }}</span>
            </li>
          </ul>

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

          <div class="mt-5 flex flex-wrap gap-2">
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
        </section>
      </div>
    </template>

    <p v-if="error" class="mt-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>
  </div>
</template>