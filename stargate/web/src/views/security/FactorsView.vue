<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import { useNoticeStore } from '@/stores/notice'
import { securityApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AuthFactor } from '@/lib/api/types'
import { factorTypeDescriptions, factorTypeLabels } from '@/lib/enums'
import { relative, remaining } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import SecurityNav from '@/components/SecurityNav.vue'

const notice = useNoticeStore()

const factors = ref<AuthFactor[]>([])
const loading = ref(true)
const error = ref('')
const busy = ref(false)
const pendingDelete = ref<AuthFactor | null>(null)

const addable = [
  { type: 5, label: 'recovery code', note: 'Required before any other factor can be added.' },
  { type: 3, label: 'authenticator app', note: 'Rotating six-digit codes (TOTP).' },
  { type: 0, label: 'password', note: 'A second password for this account.' },
  { type: 1, label: 'email code', note: 'A code sent to a verified contact.' },
  { type: 7, label: 'passkey', note: 'Register a credential from this device next.' },
  { type: 4, label: 'PIN', note: 'A short numeric secret.' },
]

const chosenType = ref(3)
const secret = ref('')
const created = ref<AuthFactor | null>(null)

const hasRecovery = computed(() => factors.value.some((f) => f.type === 5))

async function load() {
  loading.value = true
  error.value = ''
  try {
    factors.value = await securityApi.factors()
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : 'Factors could not be loaded.'
  } finally {
    loading.value = false
  }
}

async function create() {
  busy.value = true
  error.value = ''
  try {
    const factor = await securityApi.createFactor(chosenType.value, secret.value || undefined)
    created.value = factor
    secret.value = ''
    notice.ok(`Added ${factorTypeLabels[factor.type] ?? 'factor'}.`)
    await load()
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : 'The factor could not be added.'
  } finally {
    busy.value = false
  }
}

async function toggle(factor: AuthFactor) {
  busy.value = true
  error.value = ''
  try {
    if (factor.enabled_at) {
      await securityApi.disableFactor(factor.id)
      notice.ok(`${factorTypeLabels[factor.type] ?? 'Factor'} disabled.`)
    } else {
      await securityApi.enableFactor(factor.id)
      notice.ok(`${factorTypeLabels[factor.type] ?? 'Factor'} enabled.`)
    }
    await load()
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : 'The factor could not be updated.'
  } finally {
    busy.value = false
  }
}

async function remove() {
  const target = pendingDelete.value
  if (!target) return
  busy.value = true
  try {
    await securityApi.deleteFactor(target.id)
    notice.ok(`Deleted ${factorTypeLabels[target.type] ?? 'factor'}.`)
    pendingDelete.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : 'The factor could not be deleted.')
  } finally {
    busy.value = false
  }
}

function createdSecret(): string {
  const response = created.value?.created_response
  if (!response) return ''
  const code = response.recovery_code ?? response.secret
  return typeof code === 'string' ? code : ''
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      title="Sign-In Factors"
      description="Each factor counts as a step. The risk engine asks for more steps when a sign-in looks unfamiliar."
    />

    <SecurityNav />

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
      <ConsolePanel title="Configured Factors" :note="`${factors.length} total`">
        <div v-if="loading" class="py-6 text-xs text-faint">Reading factor table…</div>

        <p v-else-if="!factors.length" class="voice py-6">
          No factors yet. Start with a recovery code, then add an authenticator or passkey.
        </p>

        <ul v-else class="divide-y divide-line-soft">
          <li
            v-for="factor in factors"
            :key="factor.id"
            class="flex flex-wrap items-start justify-between gap-3 py-3 first:pt-0 last:pb-0"
          >
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <span class="text-xs text-base-content">
                  {{ factorTypeLabels[factor.type] ?? 'factor' }}
                </span>
                <span
                  class="chip"
                  :class="factor.enabled_at ? 'text-success' : 'text-warning'"
                >
                  {{ factor.enabled_at ? 'enabled' : 'disabled' }}
                </span>
                <span class="chip">weight {{ factor.trustworthy }}</span>
              </div>
              <p class="mt-1 max-w-md voice text-[12px] text-faint">
                {{ factorTypeDescriptions[factor.type] }}
              </p>
              <p class="mt-1 text-2xs text-faint">
                added {{ relative(factor.created_at) }}
                <template v-if="factor.enabled_at">
                  · enabled {{ relative(factor.enabled_at) }}
                </template>
                <template v-if="factor.expired_at">
                  · {{ remaining(factor.expired_at) }}
                </template>
              </p>
            </div>
            <div class="flex items-center gap-3">
              <button
                type="button"
                class="text-2xs text-dim hover:text-base-content"
                :disabled="busy"
                @click="toggle(factor)"
              >
                {{ factor.enabled_at ? 'disable' : 'enable' }}
              </button>
              <button
                type="button"
                class="text-2xs text-dim hover:text-error"
                :disabled="busy"
                @click="pendingDelete = factor"
              >
                Delete
              </button>
            </div>
          </li>
        </ul>
      </ConsolePanel>

      <div class="space-y-6">
        <ConsolePanel title="Add a Factor">
          <p v-if="!hasRecovery" class="mb-4 border-l border-warning pl-3 text-2xs leading-relaxed text-warning">
            Add a recovery code first. The server refuses every other factor until one is enabled.
          </p>

          <label for="factor-type" class="field-label">Factor</label>
          <select id="factor-type" v-model="chosenType" class="select select-sm">
            <option v-for="option in addable" :key="option.type" :value="option.type">
              {{ option.label }}
            </option>
          </select>
          <p class="mt-2 voice text-[12px] text-faint">
            {{ addable.find((item) => item.type === chosenType)?.note }}
          </p>

          <template v-if="chosenType === 0 || chosenType === 4">
            <label for="factor-secret" class="field-label mt-4">Secret</label>
            <input
              id="factor-secret"
              v-model="secret"
              class="input input-sm"
              type="password"
              autocomplete="new-password"
            />
          </template>

          <button
            type="button"
            class="btn btn-primary btn-sm mt-4 w-full"
            :disabled="busy"
            @click="create"
          >
            {{ busy ? 'Adding…' : 'Add Factor' }}
          </button>

          <div
            v-if="created && createdSecret()"
            class="mt-4 border border-warning/50 bg-warning/5 p-3"
          >
            <p class="text-2xs uppercase tracking-[0.12em] text-warning">
              {{ created.type === 5 ? 'recovery code' : 'authenticator secret' }}
            </p>
            <p class="mt-2 break-all font-mono text-xs text-base-content">{{ createdSecret() }}</p>
            <p class="mt-2 voice text-[12px] text-faint">
              Shown once. Store it offline before leaving this page.
            </p>
            <p v-if="created.created_response?.uri" class="mt-2 break-all text-2xs text-faint">
              {{ created.created_response.uri }}
            </p>
          </div>
        </ConsolePanel>

        <ConsolePanel title="How Steps Work">
          <p class="voice text-[12px]">
            A challenge asks for a number of steps. Each satisfied factor subtracts its weight.
            In lockdown mode every enabled factor is required; in lockoff a single factor is
            enough.
          </p>
          <RouterLink to="/security/preferences" class="mt-3 inline-block text-2xs link-quiet">
            Change security mode
          </RouterLink>
        </ConsolePanel>
      </div>
    </div>

    <ConfirmAction
      v-if="pendingDelete"
      title="Delete This Factor?"
      :body="`${factorTypeLabels[pendingDelete.type] ?? 'This factor'} will be removed. Sessions that relied on it keep their tokens until they expire.`"
      action="Delete Factor"
      tone="error"
      :busy="busy"
      @cancel="pendingDelete = null"
      @confirm="remove"
    />
  </div>
</template>
