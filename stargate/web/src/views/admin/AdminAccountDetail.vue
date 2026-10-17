<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AdminAccountDetail } from '@/lib/api/types'
import { factorTypeLabels, punishmentTypeLabels } from '@/lib/enums'
import { age, relative, remaining, shortId, utcStamp } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'

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
      title: 'Revoke every session?',
      body: 'All sessions for this account are invalidated immediately. The account must sign in again.',
      action: 'Revoke Sessions',
      tone: 'error' as const,
    }
  }
  if (pendingAction.value === 'activate') {
    return {
      title: 'Activate this account?',
      body: 'Sets the activation timestamp and grants the verified permission group.',
      action: 'Activate Account',
      tone: 'neutral' as const,
    }
  }
  return {
    title: 'Delete this account?',
    body: 'The account is soft-deleted: its name stays reserved and its sessions stop working. This cannot be undone from the console.',
    action: 'Delete Account',
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
      caught instanceof StargateError ? caught.message : 'The account record could not be read.'
  } finally {
    loading.value = false
  }
}

async function confirmAction() {
  busy.value = true
  try {
    if (pendingAction.value === 'revoke') {
      await adminApi.revokeSessions(name.value)
      notice.ok('All sessions revoked.')
    } else if (pendingAction.value === 'activate') {
      await adminApi.activate(name.value)
      notice.ok('Account activated.')
    } else if (pendingAction.value === 'delete') {
      await adminApi.deleteAccount(name.value)
      notice.ok('Account soft-deleted.')
      await router.push('/admin/accounts')
      return
    }
    pendingAction.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : 'The action failed.')
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
    notice.ok('Restriction applied and sessions revoked.')
    suspendOpen.value = false
    suspendReason.value = ''
    suspendUntil.value = ''
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : 'The restriction could not be applied.')
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
      :description="account ? `@${account.name} · ${account.id}` : 'Loading account record.'"
    >
      <template #actions>
        <button type="button" class="btn btn-ghost btn-sm" @click="router.back()">Back</button>
        <button
          type="button"
          class="btn btn-ghost btn-sm"
          :disabled="loading"
          @click="pendingAction = 'activate'"
        >
          Activate
        </button>
        <button
          type="button"
          class="btn btn-ghost btn-sm"
          :disabled="loading"
          @click="pendingAction = 'revoke'"
        >
          Revoke sessions
        </button>
        <button
          type="button"
          class="btn btn-error btn-sm"
          :disabled="loading"
          @click="suspendOpen = true"
        >
          Restrict
        </button>
      </template>
    </PageHeader>

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <div v-if="loading" class="py-10 text-xs text-faint">Reading account record…</div>

    <div v-else-if="detail" class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_340px]">
      <div class="space-y-6">
        <ConsolePanel v-if="restriction" title="Active Restriction">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
              <p class="text-xs text-error">
                {{ punishmentTypeLabels[restriction.type] ?? 'restricted' }}
              </p>
              <p class="voice mt-1 text-[12px] text-dim">{{ restriction.reason }}</p>
            </div>
            <div class="text-right text-2xs text-faint">
              <p>{{ remaining(restriction.expired_at) }}</p>
              <p>{{ relative(restriction.created_at) }}</p>
            </div>
          </div>
          <p v-if="restriction.blocked_permissions?.length" class="mt-3 text-2xs text-faint">
            blocked: {{ restriction.blocked_permissions.join(' · ') }}
          </p>
        </ConsolePanel>

        <ConsolePanel title="Account">
          <dl class="grid gap-x-6 gap-y-3 sm:grid-cols-2">
            <div class="flex justify-between border-b border-line-soft pb-2 text-xs">
              <dt class="text-faint">name</dt>
              <dd class="text-dim">{{ account?.name }}</dd>
            </div>
            <div class="flex justify-between border-b border-line-soft pb-2 text-xs">
              <dt class="text-faint">nick</dt>
              <dd class="text-dim">{{ account?.nick }}</dd>
            </div>
            <div class="flex justify-between border-b border-line-soft pb-2 text-xs">
              <dt class="text-faint">locale</dt>
              <dd class="text-dim">{{ account?.language }} · {{ account?.region }}</dd>
            </div>
            <div class="flex justify-between border-b border-line-soft pb-2 text-xs">
              <dt class="text-faint">activated</dt>
              <dd class="text-dim">{{ account?.activated_at ? 'yes' : 'no' }}</dd>
            </div>
            <div class="flex justify-between border-b border-line-soft pb-2 text-xs">
              <dt class="text-faint">superuser</dt>
              <dd :class="account?.is_superuser ? 'text-accent' : 'text-dim'">
                {{ account?.is_superuser ? 'yes' : 'no' }}
              </dd>
            </div>
            <div class="flex justify-between border-b border-line-soft pb-2 text-xs">
              <dt class="text-faint">created</dt>
              <dd class="text-dim">{{ utcStamp(account?.created_at).slice(0, 10) }}</dd>
            </div>
          </dl>
        </ConsolePanel>

        <ConsolePanel title="Contacts" :note="`${detail.contacts.length}`">
          <p v-if="!detail.contacts.length" class="voice py-3 text-[12px]">
            No contacts on this account.
          </p>
          <ul v-else class="divide-y divide-line-soft">
            <li
              v-for="contact in detail.contacts"
              :key="contact.id"
              class="flex flex-wrap items-center justify-between gap-2 py-2 first:pt-0 last:pb-0"
            >
              <div class="flex flex-wrap items-center gap-2">
                <span class="break-all text-xs text-base-content">{{ contact.content }}</span>
                <span v-if="contact.is_primary" class="chip text-primary">primary</span>
                <span v-if="contact.is_public" class="chip">public</span>
                <span class="chip" :class="contact.verified_at ? 'text-success' : 'text-warning'">
                  {{ contact.verified_at ? 'verified' : 'unverified' }}
                </span>
              </div>
              <span class="text-2xs text-faint">{{ relative(contact.created_at) }}</span>
            </li>
          </ul>
        </ConsolePanel>

        <ConsolePanel title="Sign-In Factors" :note="`${detail.auth_factors.length} · ${detail.passkey_count} passkeys`">
          <p v-if="!detail.auth_factors.length" class="voice py-3 text-[12px]">
            This account has no factors and cannot start a challenge.
          </p>
          <ul v-else class="divide-y divide-line-soft">
            <li
              v-for="factor in detail.auth_factors"
              :key="factor.id"
              class="flex flex-wrap items-center justify-between gap-2 py-2 first:pt-0 last:pb-0"
            >
              <div class="flex flex-wrap items-center gap-2">
                <span class="text-xs text-base-content">
                  {{ factorTypeLabels[factor.type] ?? 'factor' }}
                </span>
                <span class="chip" :class="factor.enabled_at ? 'text-success' : 'text-warning'">
                  {{ factor.enabled_at ? 'enabled' : 'disabled' }}
                </span>
                <span class="chip">weight {{ factor.trustworthy }}</span>
                <span v-if="factor.has_secret" class="chip">secret set</span>
              </div>
              <span class="text-2xs text-faint">{{ relative(factor.created_at) }}</span>
            </li>
          </ul>
        </ConsolePanel>
      </div>

      <div class="space-y-6">
        <ConsolePanel title="Footprint">
          <dl class="space-y-3 text-xs">
            <div class="flex justify-between">
              <dt class="text-faint">sessions</dt>
              <dd class="tabular-nums text-dim">{{ detail.active_session_count }}</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-faint">devices</dt>
              <dd class="tabular-nums text-dim">{{ detail.active_device_count }}</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-faint">connections</dt>
              <dd class="tabular-nums text-dim">{{ detail.connection_count }}</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-faint">punishments</dt>
              <dd class="tabular-nums text-dim">{{ detail.active_punishments.length }}</dd>
            </div>
          </dl>
        </ConsolePanel>

        <ConsolePanel title="Record">
          <p class="text-2xs text-faint">account id</p>
          <p class="mt-1 break-all text-xs text-dim">{{ account?.id }}</p>
          <p class="mt-3 text-2xs text-faint">last updated</p>
          <p class="mt-1 text-xs text-dim">{{ age(account?.updated_at) }} ago</p>
          <p class="mt-3 text-2xs text-faint">short reference</p>
          <p class="mt-1 text-xs text-dim">{{ shortId(account?.id) }}</p>
        </ConsolePanel>

        <ConsolePanel title="Danger">
          <button
            type="button"
            class="btn btn-error btn-sm w-full"
            :disabled="busy"
            @click="pendingAction = 'delete'"
          >
            Delete account
          </button>
          <p class="voice mt-3 text-[12px] text-faint">
            Soft-deletes the row. The name stays reserved and the account can no longer sign in.
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
          <h2 id="suspend-title" class="text-sm text-base-content">Restrict This Account</h2>
          <p class="voice mt-1 text-[12px]">
            Applied immediately. Sessions are revoked unless you turn that off in the API client.
          </p>
        </div>
        <div class="px-5 py-4">
          <label for="suspend-type" class="field-label">Type</label>
          <select id="suspend-type" v-model="suspendType" class="select select-sm">
            <option :value="1">block sign-in</option>
            <option :value="2">disable account</option>
          </select>

          <label for="suspend-reason" class="field-label mt-4">Reason</label>
          <textarea
            id="suspend-reason"
            v-model="suspendReason"
            class="textarea textarea-sm"
            rows="3"
            placeholder="Shown to the account owner."
          />

          <label for="suspend-until" class="field-label mt-4">Expires</label>
          <input
            id="suspend-until"
            v-model="suspendUntil"
            class="input input-sm"
            type="datetime-local"
          />
          <p class="mt-1 text-2xs text-faint">Leave empty for an indefinite restriction.</p>
        </div>
        <div class="flex items-center justify-end gap-2 border-t border-line px-5 py-3">
          <button type="button" class="btn btn-ghost btn-xs" @click="suspendOpen = false">
            Cancel
          </button>
          <button
            type="button"
            class="btn btn-error btn-xs"
            :disabled="busy || !suspendReason.trim()"
            @click="suspend"
          >
            Apply restriction
          </button>
        </div>
      </div>
      <button
        type="button"
        class="modal-backdrop bg-black/60"
        aria-label="Cancel"
        @click="suspendOpen = false"
      />
    </dialog>
  </div>
</template>
