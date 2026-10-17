<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AdminAccountDetail } from '@/lib/api/types'
import { factorTypeLabel, punishmentTypeLabel } from '@/lib/enums'
import { age, relative, remaining, shortId, utcStamp } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const notice = useNoticeStore()

const detail = ref<AdminAccountDetail | null>(null)
const loading = ref(true)
const error = ref('')
const busy = ref(false)

const suspendOpen = ref(false)
const suspendReason = ref('')
const suspendType = ref(1)
const suspendUntil = ref('')

const pendingAction = ref<'revoke' | 'activate' | 'delete' | null>(null)

const name = computed(() => String(route.params.name ?? ''))
const account = computed(() => detail.value?.account)

const restriction = computed(() => detail.value?.active_punishment ?? null)

const confirmCopy = computed(() => {
  if (pendingAction.value === 'revoke') {
    return {
      title: t('admin.accountDetail.confirmRevokeTitle'),
      body: t('admin.accountDetail.confirmRevokeBody'),
      action: t('admin.accountDetail.confirmRevokeAction'),
      tone: 'error' as const,
    }
  }
  if (pendingAction.value === 'activate') {
    return {
      title: t('admin.accountDetail.confirmActivateTitle'),
      body: t('admin.accountDetail.confirmActivateBody'),
      action: t('admin.accountDetail.confirmActivateAction'),
      tone: 'neutral' as const,
    }
  }
  return {
    title: t('admin.accountDetail.confirmDeleteTitle'),
    body: t('admin.accountDetail.confirmDeleteBody'),
    action: t('admin.accountDetail.confirmDeleteAction'),
    tone: 'error' as const,
  }
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    detail.value = await adminApi.account(name.value)
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('admin.accountDetail.errorLoad')
  } finally {
    loading.value = false
  }
}

async function confirmAction() {
  busy.value = true
  try {
    if (pendingAction.value === 'revoke') {
      await adminApi.revokeSessions(name.value)
      notice.ok(t('admin.accountDetail.noticeRevoked'))
    } else if (pendingAction.value === 'activate') {
      await adminApi.activate(name.value)
      notice.ok(t('admin.accountDetail.noticeActivated'))
    } else if (pendingAction.value === 'delete') {
      await adminApi.deleteAccount(name.value)
      notice.ok(t('admin.accountDetail.noticeDeleted'))
      await router.push('/admin/accounts')
      return
    }
    pendingAction.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.accountDetail.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function suspend() {
  if (!suspendReason.value.trim()) return
  busy.value = true
  try {
    await adminApi.suspend(name.value, {
      reason: suspendReason.value.trim(),
      type: suspendType.value,
      expired_at: suspendUntil.value ? new Date(suspendUntil.value).toISOString() : undefined,
      revoke_sessions: true,
    })
    notice.ok(t('admin.accountDetail.noticeRestricted'))
    suspendOpen.value = false
    suspendReason.value = ''
    suspendUntil.value = ''
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.accountDetail.errorRestricted'))
  } finally {
    busy.value = false
  }
}

watch(() => route.params.name, load)
onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      :title="account?.nick || name"
      :description="
        account ? `@${account.name} · ${account.id}` : t('admin.accountDetail.loadingDescription')
      "
    >
      <template #actions>
        <button type="button" class="btn btn-ghost btn-sm" @click="router.back()">
          {{ t('common.back') }}
        </button>
        <button
          type="button"
          class="btn btn-ghost btn-sm"
          :disabled="loading"
          @click="pendingAction = 'activate'"
        >
          {{ t('admin.accountDetail.activate') }}
        </button>
        <button
          type="button"
          class="btn btn-ghost btn-sm"
          :disabled="loading"
          @click="pendingAction = 'revoke'"
        >
          {{ t('admin.accountDetail.revokeSessions') }}
        </button>
        <button
          type="button"
          class="btn btn-error btn-sm"
          :disabled="loading"
          @click="suspendOpen = true"
        >
          {{ t('admin.accountDetail.restrict') }}
        </button>
      </template>
    </PageHeader>

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <LoadingSkeleton v-if="loading" compact :rows="5" :columns="[3, 3, 2, 1]" header />

    <div v-else-if="detail" class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_340px]">
      <div class="space-y-6">
        <ConsolePanel v-if="restriction" :title="t('admin.accountDetail.activeRestriction')">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
              <p class="text-xs text-error">
                {{ punishmentTypeLabel(restriction.type) }}
              </p>
              <p class="voice mt-1 text-[12px] text-dim">{{ restriction.reason }}</p>
            </div>
            <div class="text-right text-2xs text-faint">
              <p>{{ remaining(restriction.expired_at) }}</p>
              <p>{{ relative(restriction.created_at) }}</p>
            </div>
          </div>
          <p v-if="restriction.blocked_permissions?.length" class="mt-3 text-2xs text-faint">
            {{
              t('admin.accountDetail.blocked', {
                list: restriction.blocked_permissions.join(' · '),
              })
            }}
          </p>
        </ConsolePanel>

        <ConsolePanel :title="t('admin.accountDetail.panelAccount')">
          <dl class="grid gap-x-6 gap-y-3 sm:grid-cols-2">
            <div class="flex justify-between pb-2 text-xs">
              <dt class="text-faint">{{ t('common.name') }}</dt>
              <dd class="text-dim">{{ account?.name }}</dd>
            </div>
            <div class="flex justify-between pb-2 text-xs">
              <dt class="text-faint">{{ t('admin.accountDetail.nick') }}</dt>
              <dd class="text-dim">{{ account?.nick }}</dd>
            </div>
            <div class="flex justify-between pb-2 text-xs">
              <dt class="text-faint">{{ t('admin.accountDetail.locale') }}</dt>
              <dd class="text-dim">{{ account?.language }} · {{ account?.region }}</dd>
            </div>
            <div class="flex justify-between pb-2 text-xs">
              <dt class="text-faint">{{ t('admin.accountDetail.activated') }}</dt>
              <dd class="text-dim">
                {{ account?.activated_at ? t('common.yes') : t('common.no') }}
              </dd>
            </div>
            <div class="flex justify-between pb-2 text-xs">
              <dt class="text-faint">{{ t('admin.accountDetail.superuser') }}</dt>
              <dd :class="account?.is_superuser ? 'text-accent' : 'text-dim'">
                {{ account?.is_superuser ? t('common.yes') : t('common.no') }}
              </dd>
            </div>
            <div class="flex justify-between pb-2 text-xs">
              <dt class="text-faint">{{ t('common.created') }}</dt>
              <dd class="text-dim">{{ utcStamp(account?.created_at).slice(0, 10) }}</dd>
            </div>
          </dl>
        </ConsolePanel>

        <ConsolePanel
          :title="t('admin.accountDetail.panelContacts')"
          :note="`${detail.contacts.length}`"
        >
          <p v-if="!detail.contacts.length" class="voice py-3 text-[12px]">
            {{ t('admin.accountDetail.noContacts') }}
          </p>
          <ul v-else>
            <li
              v-for="contact in detail.contacts"
              :key="contact.id"
              class="flex flex-wrap items-center justify-between gap-2 py-2 first:pt-0 last:pb-0"
            >
              <div class="flex flex-wrap items-center gap-2">
                <span class="break-all text-xs text-base-content">{{ contact.content }}</span>
                <span v-if="contact.is_primary" class="chip text-primary">
                  {{ t('admin.accountDetail.primary') }}
                </span>
                <span v-if="contact.is_public" class="chip">
                  {{ t('admin.accountDetail.public') }}
                </span>
                <span class="chip" :class="contact.verified_at ? 'text-success' : 'text-warning'">
                  {{
                    contact.verified_at
                      ? t('admin.accountDetail.verified')
                      : t('admin.accountDetail.unverified')
                  }}
                </span>
              </div>
              <span class="text-2xs text-faint">{{ relative(contact.created_at) }}</span>
            </li>
          </ul>
        </ConsolePanel>

        <ConsolePanel
          :title="t('admin.accountDetail.panelFactors')"
          :note="
            `${detail.auth_factors.length} · ${t('admin.accountDetail.passkeys', {
              n: detail.passkey_count,
            })}`
          "
        >
          <p v-if="!detail.auth_factors.length" class="voice py-3 text-[12px]">
            {{ t('admin.accountDetail.noFactors') }}
          </p>
          <ul v-else>
            <li
              v-for="factor in detail.auth_factors"
              :key="factor.id"
              class="flex flex-wrap items-center justify-between gap-2 py-2 first:pt-0 last:pb-0"
            >
              <div class="flex flex-wrap items-center gap-2">
                <span class="text-xs text-base-content">
                  {{ factorTypeLabel(factor.type) }}
                </span>
                <span class="chip" :class="factor.enabled_at ? 'text-success' : 'text-warning'">
                  {{ factor.enabled_at ? t('common.enabled') : t('common.disabled') }}
                </span>
                <span class="chip">
                  {{ t('admin.accountDetail.weight', { n: factor.trustworthy }) }}
                </span>
                <span v-if="factor.has_secret" class="chip">
                  {{ t('admin.accountDetail.secretSet') }}
                </span>
              </div>
              <span class="text-2xs text-faint">{{ relative(factor.created_at) }}</span>
            </li>
          </ul>
        </ConsolePanel>
      </div>

      <div class="space-y-6">
        <ConsolePanel :title="t('admin.accountDetail.panelFootprint')">
          <dl class="space-y-3 text-xs">
            <div class="flex justify-between">
              <dt class="text-faint">{{ t('admin.accountDetail.sessions') }}</dt>
              <dd class="tabular-nums text-dim">{{ detail.active_session_count }}</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-faint">{{ t('admin.accountDetail.devices') }}</dt>
              <dd class="tabular-nums text-dim">{{ detail.active_device_count }}</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-faint">{{ t('admin.accountDetail.connections') }}</dt>
              <dd class="tabular-nums text-dim">{{ detail.connection_count }}</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-faint">{{ t('admin.accountDetail.punishments') }}</dt>
              <dd class="tabular-nums text-dim">{{ detail.active_punishments.length }}</dd>
            </div>
          </dl>
        </ConsolePanel>

        <ConsolePanel :title="t('admin.accountDetail.panelRecord')">
          <p class="text-2xs text-faint">{{ t('admin.accountDetail.accountId') }}</p>
          <p class="mt-1 break-all text-xs text-dim">{{ account?.id }}</p>
          <p class="mt-3 text-2xs text-faint">{{ t('admin.accountDetail.lastUpdated') }}</p>
          <p class="mt-1 text-xs text-dim">
            {{ t('admin.accountDetail.ago', { age: age(account?.updated_at) }) }}
          </p>
          <p class="mt-3 text-2xs text-faint">{{ t('admin.accountDetail.shortReference') }}</p>
          <p class="mt-1 text-xs text-dim">{{ shortId(account?.id) }}</p>
        </ConsolePanel>

        <ConsolePanel :title="t('admin.accountDetail.panelDanger')">
          <button
            type="button"
            class="btn btn-error btn-sm w-full"
            :disabled="busy"
            @click="pendingAction = 'delete'"
          >
            {{ t('admin.accountDetail.deleteAccount') }}
          </button>
          <p class="voice mt-3 text-[12px] text-faint">
            {{ t('admin.accountDetail.deleteHint') }}
          </p>
        </ConsolePanel>
      </div>
    </div>

    <ConfirmAction
      v-if="pendingAction"
      :title="confirmCopy.title"
      :body="confirmCopy.body"
      :action="confirmCopy.action"
      :tone="confirmCopy.tone"
      :busy="busy"
      @cancel="pendingAction = null"
      @confirm="confirmAction"
    />

    <dialog v-if="suspendOpen" class="modal modal-open" aria-modal="true" aria-labelledby="suspend-title">
      <div class="modal-box max-w-md rounded-[2px] border border-line bg-base-100 p-0 shadow-none">
        <div class="border-b border-line px-5 py-4">
          <h2 id="suspend-title" class="text-sm text-base-content">
            {{ t('admin.accountDetail.suspendTitle') }}
          </h2>
          <p class="voice mt-1 text-[12px]">
            {{ t('admin.accountDetail.suspendIntro') }}
          </p>
        </div>
        <div class="px-5 py-4">
          <label for="suspend-type" class="field-label">{{ t('common.type') }}</label>
          <select id="suspend-type" v-model="suspendType" class="select select-sm">
            <option :value="1">{{ t('admin.accountDetail.blockSignIn') }}</option>
            <option :value="2">{{ t('admin.accountDetail.disableAccount') }}</option>
          </select>

          <label for="suspend-reason" class="field-label mt-4">
            {{ t('admin.accountDetail.reason') }}
          </label>
          <textarea
            id="suspend-reason"
            v-model="suspendReason"
            class="textarea textarea-sm"
            rows="3"
            :placeholder="t('admin.accountDetail.reasonPlaceholder')"
          />

          <label for="suspend-until" class="field-label mt-4">{{ t('common.expires') }}</label>
          <input
            id="suspend-until"
            v-model="suspendUntil"
            class="input input-sm"
            type="datetime-local"
          />
          <p class="mt-1 text-2xs text-faint">{{ t('admin.accountDetail.expiresHint') }}</p>
        </div>
        <div class="flex items-center justify-end gap-2 border-t border-line px-5 py-3">
          <button type="button" class="btn btn-ghost btn-xs" @click="suspendOpen = false">
            {{ t('common.cancel') }}
          </button>
          <button
            type="button"
            class="btn btn-error btn-xs"
            :disabled="busy || !suspendReason.trim()"
            @click="suspend"
          >
            {{ t('admin.accountDetail.applyRestriction') }}
          </button>
        </div>
      </div>
      <button
        type="button"
        class="modal-backdrop bg-black/60"
        :aria-label="t('common.cancel')"
        @click="suspendOpen = false"
      />
    </dialog>
  </div>
</template>
