<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Launch16 } from '@carbon/icons-vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'

import { useSessionStore } from '@/stores/session'
import { useUiStore } from '@/stores/ui'
import { authApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { CloudFileRef, DeviceCodeStatus } from '@/lib/api/types'
import { remaining } from '@/lib/format'
import AppMark from '@/components/AppMark.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const session = useSessionStore()
const ui = useUiStore()

const code = ref(typeof route.query.code === 'string' ? route.query.code : '')
const status = ref<DeviceCodeStatus | null>(null)
const loading = ref(false)
const busy = ref(false)
const error = ref('')

/**
 * The device flow's verification URL carries the code as `?code=`, so the page
 * opens already knowing it. Without one the reader types the code the device
 * shows; both paths land in the same lookup.
 */
const entered = ref(code.value)

const scopeLabels = computed<Record<string, string>>(() => ({
  openid: t('authorize.scopeOpenid'),
  profile: t('authorize.scopeProfile'),
  email: t('authorize.scopeEmail'),
  offline_access: t('authorize.scopeOfflineAccess'),
  '*': t('authorize.scopeAll'),
}))

const name = computed(
  () => status.value?.client_name || status.value?.client_slug || status.value?.client_id || '',
)

const backdrop = computed<CloudFileRef | null>(() => status.value?.background ?? null)

watch(backdrop, (value) => ui.setAppBackdrop(value?.url ?? ''))
onBeforeUnmount(() => ui.setAppBackdrop(''))

// One word decides what the page is: an input, a decision, or a closed
// request. The provider only answers "pending" while the code can still be
// acted on, so anything else is terminal.
const phase = computed<'lookup' | 'pending' | 'closed' | 'missing'>(() => {
  if (loading.value) return 'lookup'
  if (!status.value) return 'missing'
  return status.value.status === 'pending' ? 'pending' : 'closed'
})

const terminal = computed(() => {
  const value = status.value?.status
  if (value === 'approved') {
    return { tone: 'success', title: t('device.terminalApproved'), hint: t('device.terminalApprovedHint') }
  }
  if (value === 'declined') {
    return { tone: 'error', title: t('device.terminalDeclined'), hint: t('device.terminalDeclinedHint') }
  }
  if (value === 'expired') {
    return { tone: 'warning', title: t('device.terminalExpired'), hint: t('device.terminalExpiredHint') }
  }
  return { tone: 'warning', title: t('device.terminalOther'), hint: t('device.terminalOtherHint') }
})

async function lookup(raw: string) {
  const value = raw.trim()
  if (!value) return
  loading.value = true
  error.value = ''
  try {
    status.value = await authApi.deviceCodeStatus(value)
    // Keep the address shareable and reload-safe: the code is the only state.
    if (route.query.code !== value) {
      await router.replace({ query: { ...route.query, code: value } })
    }
  } catch (caught) {
    status.value = null
    error.value = caught instanceof StargateError ? caught.message : t('device.errorLookup')
  } finally {
    loading.value = false
  }
}

async function decide(approve: boolean) {
  if (!status.value) return
  busy.value = true
  error.value = ''
  try {
    if (approve) await authApi.deviceCodeApprove(status.value.user_code)
    else await authApi.deviceCodeDecline(status.value.user_code)
    status.value = {
      ...status.value,
      status: approve ? 'approved' : 'declined',
    }
  } catch (caught) {
    error.value =
      caught instanceof StargateError
        ? caught.message
        : approve
          ? t('device.errorApprove')
          : t('device.errorDecline')
  } finally {
    busy.value = false
  }
}

onMounted(() => {
  if (code.value) void lookup(code.value)
})
</script>

<template>
  <div>
    <!-- The page asks a question only while the code can be answered; once the
         request is closed, the headline is the outcome, not an invitation. -->
    <h1 class="title mt-2 text-2xl text-base-content">
      {{
        phase === 'pending'
          ? t('device.approveTitle')
          : phase === 'closed'
            ? terminal.title
            : t('device.loadingTitle')
      }}
    </h1>
    <p v-if="phase === 'pending'" class="voice mt-2">
      {{ t('device.intro') }}
      <template v-if="session.identity?.name">
        {{ t('authorize.introPrefix') }}
        <span class="text-base-content">{{ session.identity.name }}</span
        >{{ t('authorize.introSuffix') }}
      </template>
    </p>
    <p v-else-if="phase === 'closed'" class="voice mt-2">{{ terminal.hint }}</p>
    <p v-else class="voice mt-2">{{ t('device.intro') }}</p>

    <!-- The code itself is the only state the page needs, so it is the first
         thing on the page whenever the address did not carry one. -->
    <form v-if="!status" class="mt-6 border border-line p-4" @submit.prevent="lookup(entered)">
      <label for="device-code" class="field-label">{{ t('device.codeLabel') }}</label>
      <div class="flex flex-wrap gap-2">
        <input
          id="device-code"
          v-model="entered"
          class="input input-sm max-w-[220px] font-mono tracking-[0.12em] uppercase"
          :placeholder="t('device.codePlaceholder')"
          autocomplete="one-time-code"
          autocapitalize="characters"
          spellcheck="false"
        />
        <button type="submit" class="btn btn-primary btn-sm" :disabled="loading || !entered.trim()">
          {{ loading ? t('device.checking') : t('device.continue') }}
        </button>
      </div>
      <p class="voice mt-2 text-[12px] text-faint">{{ t('device.codeHint') }}</p>
    </form>

    <div v-if="loading" class="mt-6 border border-line" aria-hidden="true">
      <span class="skeleton mx-4 my-2.5 h-2 w-24" />
      <div class="space-y-3 border-t border-line px-4 py-3">
        <span class="skeleton h-3 w-2/3" />
        <span class="skeleton h-3 w-1/2" />
      </div>
    </div>

    <template v-else-if="status && phase === 'pending'">
      <div class="mt-6 border border-line sm:grid sm:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]">
        <section class="border-b border-line bg-base-200/40 p-4 sm:border-b-0 sm:border-r">
          <div class="flex items-start gap-3">
            <AppMark :picture="status.picture" :name="name" :size="40" />
            <div class="min-w-0">
              <p class="text-[15px] text-base-content">{{ name }}</p>
              <p class="text-2xs text-faint">{{ t('device.requestedAccess') }}</p>
            </div>
          </div>

          <p class="eyebrow mt-5">{{ t('device.codeLabel') }}</p>
          <p class="mt-1 font-mono text-lg tracking-[0.18em] text-base-content">
            {{ status.user_code }}
          </p>
          <p class="mt-1 text-2xs text-faint">
            {{ t('device.expiresIn', { time: remaining(status.expires_at) }) }}
          </p>
        </section>

        <section class="p-4">
          <p class="eyebrow">{{ t('authorize.requestedAccess') }}</p>
          <ul class="mt-2">
            <li v-for="scope in status.scopes" :key="scope" class="flex items-baseline gap-3 py-2">
              <span class="text-2xs text-faint">{{ scope }}</span>
              <span class="text-xs text-base-content">{{ scopeLabels[scope] ?? scope }}</span>
            </li>
          </ul>

          <p v-if="status.verification_uri" class="voice mt-3 text-[12px]">
            <a
              :href="status.verification_uri"
              class="link-quiet inline-flex items-center gap-1.5"
              target="_blank"
              rel="noreferrer"
            >
              <Launch16 aria-hidden="true" />
              {{ status.verification_uri }}
            </a>
          </p>

          <div class="mt-5 flex flex-wrap gap-2">
            <button type="button" class="btn btn-primary btn-sm" :disabled="busy" @click="decide(true)">
              {{ busy ? t('device.submitting') : t('device.approve') }}
            </button>
            <button type="button" class="btn btn-ghost btn-sm" :disabled="busy" @click="decide(false)">
              {{ t('device.deny') }}
            </button>
          </div>
        </section>
      </div>
    </template>

    <!-- Approved, declined, expired, or already closed: nothing is left to
         decide, and the device reports the outcome on its own. -->
    <div v-else-if="status" class="mt-6 border border-line p-4">
      <p class="eyebrow" :class="`text-${terminal.tone}`">{{ status.user_code }}</p>
      <p class="voice mt-1">
        {{ status.client_name || status.client_slug || status.client_id }}
      </p>
    </div>

    <p v-if="error" class="mt-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>
  </div>
</template>