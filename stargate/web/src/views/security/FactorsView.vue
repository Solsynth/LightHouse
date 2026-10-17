<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { useNoticeStore } from '@/stores/notice'
import { securityApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AuthFactor } from '@/lib/api/types'
import { factorTypeDescription, factorTypeLabel } from '@/lib/enums'
import { relative, remaining } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import SecurityNav from '@/components/SecurityNav.vue'

const { t } = useI18n()
const notice = useNoticeStore()

const factors = ref<AuthFactor[]>([])
const loading = ref(true)
const error = ref('')
const busy = ref(false)
const pendingDelete = ref<AuthFactor | null>(null)

const addable = computed(() => [
  { type: 5, label: factorTypeLabel(5), note: t('factors.recoveryNote') },
  { type: 3, label: factorTypeLabel(3), note: t('factors.authenticatorNote') },
  { type: 0, label: factorTypeLabel(0), note: t('factors.passwordNote') },
  { type: 1, label: factorTypeLabel(1), note: t('factors.emailCodeNote') },
  { type: 7, label: factorTypeLabel(7), note: t('factors.passkeyNote') },
  { type: 4, label: factorTypeLabel(4), note: t('factors.pinNote') },
])

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
    error.value = caught instanceof StargateError ? caught.message : t('factors.errorLoad')
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
    notice.ok(t('factors.noticeAdded', { factor: factorTypeLabel(factor.type) }))
    await load()
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('factors.errorAdd')
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
      notice.ok(t('factors.noticeDisabled', { factor: factorTypeLabel(factor.type) }))
    } else {
      await securityApi.enableFactor(factor.id)
      notice.ok(t('factors.noticeEnabled', { factor: factorTypeLabel(factor.type) }))
    }
    await load()
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('factors.errorUpdate')
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
    notice.ok(t('factors.noticeDeleted', { factor: factorTypeLabel(target.type) }))
    pendingDelete.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('factors.errorDelete'))
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
      :title="t('factors.title')"
      :description="t('factors.description')"
    />

    <SecurityNav />

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
      <ConsolePanel :title="t('factors.configured')" :note="t('factors.totalNote', { n: factors.length })">
        <div v-if="loading" class="py-6 text-xs text-faint">{{ t('factors.reading') }}</div>

        <p v-else-if="!factors.length" class="voice py-6">
          {{ t('factors.empty') }}
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
                  {{ factorTypeLabel(factor.type) }}
                </span>
                <span
                  class="chip"
                  :class="factor.enabled_at ? 'text-success' : 'text-warning'"
                >
                  {{ factor.enabled_at ? t('common.enabled') : t('common.disabled') }}
                </span>
                <span class="chip">{{ t('factors.weight', { n: factor.trustworthy }) }}</span>
              </div>
              <p class="mt-1 max-w-md voice text-[12px] text-faint">
                {{ factorTypeDescription(factor.type) }}
              </p>
              <p class="mt-1 text-2xs text-faint">
                {{ t('factors.added', { time: relative(factor.created_at) }) }}
                <template v-if="factor.enabled_at">
                  · {{ t('factors.enabledAt', { time: relative(factor.enabled_at) }) }}
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
                {{ factor.enabled_at ? t('factors.disable') : t('factors.enable') }}
              </button>
              <button
                type="button"
                class="text-2xs text-dim hover:text-error"
                :disabled="busy"
                @click="pendingDelete = factor"
              >
                {{ t('common.delete') }}
              </button>
            </div>
          </li>
        </ul>
      </ConsolePanel>

      <div class="space-y-6">
        <ConsolePanel :title="t('factors.addFactor')">
          <p v-if="!hasRecovery" class="mb-4 border-l border-warning pl-3 text-2xs leading-relaxed text-warning">
            {{ t('factors.recoveryFirst') }}
          </p>

          <label for="factor-type" class="field-label">{{ t('factors.factor') }}</label>
          <select id="factor-type" v-model="chosenType" class="select select-sm">
            <option v-for="option in addable" :key="option.type" :value="option.type">
              {{ option.label }}
            </option>
          </select>
          <p class="mt-2 voice text-[12px] text-faint">
            {{ addable.find((item) => item.type === chosenType)?.note }}
          </p>

          <template v-if="chosenType === 0 || chosenType === 4">
            <label for="factor-secret" class="field-label mt-4">{{ t('factors.secret') }}</label>
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
            {{ busy ? t('factors.adding') : t('factors.addAction') }}
          </button>

          <div
            v-if="created && createdSecret()"
            class="mt-4 border border-warning/50 bg-warning/5 p-3"
          >
            <p class="text-2xs uppercase tracking-[0.12em] text-warning">
              {{ created.type === 5 ? t('factors.recoveryCode') : t('factors.authenticatorSecret') }}
            </p>
            <p class="mt-2 break-all font-mono text-xs text-base-content">{{ createdSecret() }}</p>
            <p class="mt-2 voice text-[12px] text-faint">
              {{ t('factors.shownOnce') }}
            </p>
            <p v-if="created.created_response?.uri" class="mt-2 break-all text-2xs text-faint">
              {{ created.created_response.uri }}
            </p>
          </div>
        </ConsolePanel>

        <ConsolePanel :title="t('factors.howSteps')">
          <p class="voice text-[12px]">
            {{ t('factors.howStepsBody') }}
          </p>
          <RouterLink to="/security/preferences" class="mt-3 inline-block text-2xs link-quiet">
            {{ t('factors.changeMode') }}
          </RouterLink>
        </ConsolePanel>
      </div>
    </div>

    <ConfirmAction
      v-if="pendingDelete"
      :title="t('factors.deleteTitle')"
      :body="t('factors.deleteBody', { factor: factorTypeLabel(pendingDelete.type) })"
      :action="t('factors.deleteAction')"
      tone="error"
      :busy="busy"
      @cancel="pendingDelete = null"
      @confirm="remove"
    />
  </div>
</template>
