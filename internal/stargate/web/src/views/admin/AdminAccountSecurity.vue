<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { useNoticeStore } from '@/stores/notice'
import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type {
  AdminAuthFactor,
  AdminDevice,
  AuthSession,
  Connection,
  Passkey,
} from '@/lib/api/types'
import { factorTypeLabel, sessionTypeLabel } from '@/lib/enums'
import { describeAgent, place, relative, remaining, shortId } from '@/lib/format'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import TextField from '@/components/TextField.vue'

const props = defineProps<{ name: string }>()
const { t } = useI18n()
const notice = useNoticeStore()

const sessions = ref<AuthSession[]>([])
const devices = ref<AdminDevice[]>([])
const factors = ref<AdminAuthFactor[]>([])
const passkeys = ref<Passkey[]>([])
const connections = ref<Connection[]>([])
const loading = ref(true)
const busy = ref(false)
const error = ref('')

const renaming = ref<AdminDevice | null>(null)
const deviceLabel = ref('')
const resetOpen = ref(false)
const factorOpen = ref(false)
const factorDraft = ref({ type: 1, secret: '', code: '', enable: true })
const newPassword = ref('')
const resetRevoke = ref(true)
const pending = ref<{ kind: 'session' | 'device' | 'deviceSessions' | 'passkey' | 'factor'; id: string; label: string } | null>(null)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [sessionPage, devicePage, factorList, passkeyList, connectionList] = await Promise.all([
      adminApi.accountSessions(props.name, { take: 50 }),
      adminApi.accountDevices(props.name, { take: 50 }),
      adminApi.accountFactors(props.name),
      adminApi.accountPasskeys(props.name),
      adminApi.accountConnections(props.name),
    ])
    sessions.value = sessionPage.items
    devices.value = devicePage.items
    factors.value = factorList
    passkeys.value = passkeyList
    connections.value = connectionList
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('admin.account.security.errorLoad')
  } finally {
    loading.value = false
  }
}

function openRename(device: AdminDevice) {
  renaming.value = device
  deviceLabel.value = device.device_label || device.device_name
}

async function renameDevice() {
  const device = renaming.value
  if (!device || !deviceLabel.value.trim()) return
  busy.value = true
  try {
    await adminApi.renameDevice(props.name, device.id, deviceLabel.value.trim())
    notice.ok(t('admin.account.security.noticeRenamed'))
    renaming.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function confirmPending() {
  const target = pending.value
  if (!target) return
  busy.value = true
  try {
    if (target.kind === 'session') await adminApi.revokeAccountSession(props.name, target.id)
    else if (target.kind === 'device') await adminApi.deleteDevice(props.name, target.id)
    else if (target.kind === 'deviceSessions') await adminApi.revokeDeviceSessions(props.name, target.id)
    else if (target.kind === 'passkey') await adminApi.deleteAccountPasskey(props.name, target.id)
    else await adminApi.deleteFactor(props.name, target.id)
    notice.ok(t('admin.account.security.noticeRevoked'))
    pending.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function toggleFactor(factor: AdminAuthFactor) {
  busy.value = true
  try {
    if (factor.enabled_at) await adminApi.disableFactor(props.name, factor.id)
    else await adminApi.enableFactor(props.name, factor.id)
    notice.ok(t('admin.account.security.noticeFactor'))
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function createFactor() {
  busy.value = true
  try {
    await adminApi.createFactor(props.name, {
      type: factorDraft.value.type,
      secret: factorDraft.value.secret.trim() || undefined,
      code: factorDraft.value.code.trim() || undefined,
      enable: factorDraft.value.enable,
    })
    notice.ok(t('admin.account.security.noticeFactorCreated'))
    factorOpen.value = false
    factorDraft.value = { type: 1, secret: '', code: '', enable: true }
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function resetPassword() {
  if (newPassword.value.length < 8) {
    notice.fail(t('admin.account.security.passwordTooShort'))
    return
  }
  busy.value = true
  try {
    await adminApi.resetPasswordFactor(props.name, {
      new_password: newPassword.value,
      revoke_sessions: resetRevoke.value,
    })
    notice.ok(t('admin.account.security.noticePasswordReset'))
    resetOpen.value = false
    newPassword.value = ''
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

const confirmCopy = () => {
  const target = pending.value
  if (!target) return { title: '', body: '', action: '' }
  if (target.kind === 'session') {
    return {
      title: t('admin.account.security.confirmSessionTitle'),
      body: t('admin.account.security.confirmSessionBody', { label: target.label }),
      action: t('admin.account.security.revoke'),
    }
  }
  if (target.kind === 'deviceSessions') {
    return {
      title: t('admin.account.security.confirmDeviceSessionsTitle'),
      body: t('admin.account.security.confirmDeviceSessionsBody', { label: target.label }),
      action: t('admin.account.security.revoke'),
    }
  }
  if (target.kind === 'device') {
    return {
      title: t('admin.account.security.confirmDeviceTitle'),
      body: t('admin.account.security.confirmDeviceBody', { label: target.label }),
      action: t('admin.account.security.delete'),
    }
  }
  if (target.kind === 'passkey') {
    return {
      title: t('admin.account.security.confirmPasskeyTitle'),
      body: t('admin.account.security.confirmPasskeyBody', { label: target.label }),
      action: t('admin.account.security.delete'),
    }
  }
  return {
    title: t('admin.account.security.confirmFactorTitle'),
    body: t('admin.account.security.confirmFactorBody', { label: target.label }),
    action: t('admin.account.security.delete'),
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <p v-if="error" class="note note-error mb-4">{{ error }}</p>

    <LoadingSkeleton v-if="loading" :rows="4" :columns="[3, 2, 2, 1]" header />

    <template v-else>
      <ConsolePanel :title="t('admin.account.security.sessionsTitle')" :note="`${sessions.length}`">
        <p v-if="!sessions.length" class="voice py-3 text-[12px]">
          {{ t('admin.account.security.noSessions') }}
        </p>
        <ul v-else>
          <li
            v-for="session in sessions"
            :key="session.id"
            class="flex flex-wrap items-start justify-between gap-3 py-2 first:pt-0 last:pb-0"
          >
            <div class="min-w-0">
              <p class="text-xs text-base-content">
                {{ describeAgent(session.user_agent) }}
                <span v-if="session.is_current" class="chip ml-1 text-primary">
                  {{ t('admin.account.security.current') }}
                </span>
              </p>
              <p class="mt-0.5 text-2xs text-faint">
                {{ sessionTypeLabel(session.type) }} · {{ place(session.location) }} ·
                {{ session.ip_address || '—' }}
              </p>
              <p class="text-2xs text-faint" :title="session.id">
                {{ shortId(session.id) }} · {{ relative(session.last_granted_at) }} ·
                {{ remaining(session.expired_at) }}
              </p>
            </div>
            <button
              type="button"
              class="btn btn-ghost btn-xs text-error"
              :disabled="busy"
              @click="
                pending = {
                  kind: 'session',
                  id: session.id,
                  label: describeAgent(session.user_agent),
                }
              "
            >
              {{ t('admin.account.security.revoke') }}
            </button>
          </li>
        </ul>
      </ConsolePanel>

      <ConsolePanel :title="t('admin.account.security.devicesTitle')" :note="`${devices.length}`">
        <p v-if="!devices.length" class="voice py-3 text-[12px]">
          {{ t('admin.account.security.noDevices') }}
        </p>
        <ul v-else>
          <li
            v-for="device in devices"
            :key="device.id"
            class="flex flex-wrap items-start justify-between gap-3 py-2 first:pt-0 last:pb-0"
          >
            <div class="min-w-0">
              <p class="text-xs text-base-content">
                {{ device.device_label || device.device_name }}
                <span v-if="device.trusted" class="chip ml-1">{{ t('admin.account.security.trusted') }}</span>
              </p>
              <p class="mt-0.5 text-2xs text-faint">
                {{ device.category || device.platform }} · {{ device.sessions.length }}
                {{ t('admin.account.security.sessionsShort') }}
              </p>
              <p class="text-2xs text-faint">{{ shortId(device.id) }}</p>
            </div>
            <div class="flex flex-wrap items-center gap-1">
              <button type="button" class="btn btn-ghost btn-xs" :disabled="busy" @click="openRename(device)">
                {{ t('common.edit') }}
              </button>
              <button
                type="button"
                class="btn btn-ghost btn-xs"
                :disabled="busy"
                @click="pending = { kind: 'deviceSessions', id: device.id, label: device.device_label || device.device_name }"
              >
                {{ t('admin.account.security.revoke') }}
              </button>
              <button
                type="button"
                class="btn btn-ghost btn-xs text-error"
                :disabled="busy"
                @click="pending = { kind: 'device', id: device.id, label: device.device_label || device.device_name }"
              >
                {{ t('admin.account.security.delete') }}
              </button>
            </div>
          </li>
        </ul>
      </ConsolePanel>

      <ConsolePanel :title="t('admin.account.security.factorsTitle')" :note="`${factors.length}`">
        <div class="mb-3 flex justify-end gap-2">
          <button type="button" class="btn btn-ghost btn-xs" :disabled="busy" @click="factorOpen = true">
            {{ t('admin.account.security.createFactor') }}
          </button>
          <button type="button" class="btn btn-ghost btn-xs" :disabled="busy" @click="resetOpen = true">
            {{ t('admin.account.security.resetPassword') }}
          </button>
        </div>
        <ul>
          <li
            v-for="factor in factors"
            :key="factor.id"
            class="flex flex-wrap items-center justify-between gap-2 py-2 first:pt-0 last:pb-0"
          >
            <div>
              <p class="text-xs text-base-content">{{ factorTypeLabel(factor.type) }}</p>
              <p class="mt-0.5 text-2xs text-faint">
                {{ t('admin.account.security.weight', { n: factor.trustworthy }) }}
                <template v-if="factor.has_secret"> · {{ t('admin.account.security.secretSet') }}</template>
              </p>
            </div>
            <div class="flex items-center gap-1">
              <span class="chip" :class="factor.enabled_at ? 'text-success' : 'text-warning'">
                {{ factor.enabled_at ? t('common.enabled') : t('common.disabled') }}
              </span>
              <button type="button" class="btn btn-ghost btn-xs" :disabled="busy" @click="toggleFactor(factor)">
                {{ factor.enabled_at ? t('common.disable') : t('common.enable') }}
              </button>
              <button
                type="button"
                class="btn btn-ghost btn-xs text-error"
                :disabled="busy"
                @click="pending = { kind: 'factor', id: factor.id, label: factorTypeLabel(factor.type) || factor.id }"
              >
                {{ t('admin.account.security.delete') }}
              </button>
            </div>
          </li>
        </ul>
      </ConsolePanel>

      <ConsolePanel :title="t('admin.account.security.passkeysTitle')" :note="`${passkeys.length}`">
        <p v-if="!passkeys.length" class="voice py-3 text-[12px]">
          {{ t('admin.account.security.noPasskeys') }}
        </p>
        <ul v-else>
          <li
            v-for="passkey in passkeys"
            :key="passkey.id"
            class="flex flex-wrap items-center justify-between gap-2 py-2 first:pt-0 last:pb-0"
          >
            <div>
              <p class="text-xs text-base-content">{{ passkey.label }}</p>
              <p class="text-2xs text-faint">{{ relative(passkey.created_at) }}</p>
            </div>
            <button
              type="button"
              class="btn btn-ghost btn-xs text-error"
              :disabled="busy"
              @click="pending = { kind: 'passkey', id: passkey.id, label: passkey.label }"
            >
              {{ t('admin.account.security.delete') }}
            </button>
          </li>
        </ul>
      </ConsolePanel>

      <ConsolePanel :title="t('admin.account.security.connectionsTitle')" :note="`${connections.length}`">
        <p v-if="!connections.length" class="voice py-3 text-[12px]">
          {{ t('admin.account.security.noConnections') }}
        </p>
        <ul v-else>
          <li
            v-for="connection in connections"
            :key="connection.id"
            class="flex flex-wrap items-center justify-between gap-2 py-2 first:pt-0 last:pb-0"
          >
            <div class="min-w-0">
              <p class="text-xs text-base-content">
                {{ connection.provider }}
                <span v-if="connection.is_public" class="chip ml-1">{{ t('admin.account.security.public') }}</span>
              </p>
              <p class="truncate font-mono text-2xs text-faint">
                {{ connection.provided_identifier }}
              </p>
            </div>
            <p class="text-2xs text-faint">{{ relative(connection.last_used_at) }}</p>
          </li>
        </ul>
      </ConsolePanel>
    </template>

    <dialog v-if="renaming" class="modal modal-open" aria-modal="true" aria-labelledby="device-label-title">
      <div class="modal-box max-w-sm overflow-hidden p-0">
        <div class="bg-base-200/40 px-6 py-5">
          <h2 id="device-label-title" class="text-sm text-base-content">
            {{ t('admin.account.security.renameTitle') }}
          </h2>
        </div>
        <form class="px-6 py-5" @submit.prevent="renameDevice">
          <TextField id="device-label" v-model="deviceLabel" :label="t('admin.account.security.deviceLabel')" />
          <div class="mt-5 flex items-center justify-end gap-2">
            <button type="button" class="btn btn-ghost btn-xs" @click="renaming = null">
              {{ t('common.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary btn-xs" :disabled="busy || !deviceLabel.trim()">
              {{ t('common.save') }}
            </button>
          </div>
        </form>
      </div>
      <button type="button" class="modal-backdrop bg-black/60" :aria-label="t('common.cancel')" @click="renaming = null" />
    </dialog>

    <dialog v-if="factorOpen" class="modal modal-open" aria-modal="true" aria-labelledby="factor-create-title">
      <div class="modal-box max-w-sm overflow-hidden p-0">
        <div class="bg-base-200/40 px-6 py-5">
          <h2 id="factor-create-title" class="text-sm text-base-content">
            {{ t('admin.account.security.createFactor') }}
          </h2>
        </div>
        <form class="px-6 py-5" @submit.prevent="createFactor">
          <TextField id="factor-type" v-model.number="factorDraft.type" type="number" :label="t('admin.account.security.factorTypeField')" />
          <TextField id="factor-secret" v-model="factorDraft.secret" root-class="mt-3" :label="t('admin.account.security.factorSecret')" :hint="t('admin.account.security.factorSecretHint')" />
          <TextField id="factor-code" v-model="factorDraft.code" root-class="mt-3" :label="t('admin.account.security.factorCode')" />
          <label class="mt-3 flex items-center gap-2 text-xs text-dim">
            <input v-model="factorDraft.enable" class="checkbox checkbox-xs" type="checkbox" />
            {{ t('admin.account.security.factorEnable') }}
          </label>
          <div class="mt-5 flex items-center justify-end gap-2">
            <button type="button" class="btn btn-ghost btn-xs" @click="factorOpen = false">
              {{ t('common.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary btn-xs" :disabled="busy">
              {{ t('admin.account.security.createFactor') }}
            </button>
          </div>
        </form>
      </div>
      <button type="button" class="modal-backdrop bg-black/60" :aria-label="t('common.cancel')" @click="factorOpen = false" />
    </dialog>

    <dialog v-if="resetOpen" class="modal modal-open" aria-modal="true" aria-labelledby="reset-password-title">
      <div class="modal-box max-w-sm overflow-hidden p-0">
        <div class="bg-base-200/40 px-6 py-5">
          <h2 id="reset-password-title" class="text-sm text-base-content">
            {{ t('admin.account.security.resetPassword') }}
          </h2>
          <p class="voice mt-1 text-[12px]">{{ t('admin.account.security.resetHint') }}</p>
        </div>
        <form class="px-6 py-5" @submit.prevent="resetPassword">
          <TextField
            id="reset-password"
            v-model="newPassword"
            type="password"
            :label="t('admin.account.security.newPassword')"
            autocomplete="new-password"
          />
          <label class="mt-3 flex items-center gap-2 text-xs text-dim">
            <input v-model="resetRevoke" class="checkbox checkbox-xs" type="checkbox" />
            {{ t('admin.account.security.revokeAfterReset') }}
          </label>
          <div class="mt-5 flex items-center justify-end gap-2">
            <button type="button" class="btn btn-ghost btn-xs" @click="resetOpen = false">
              {{ t('common.cancel') }}
            </button>
            <button type="submit" class="btn btn-error btn-xs" :disabled="busy">
              {{ t('admin.account.security.resetPassword') }}
            </button>
          </div>
        </form>
      </div>
      <button type="button" class="modal-backdrop bg-black/60" :aria-label="t('common.cancel')" @click="resetOpen = false" />
    </dialog>

    <ConfirmAction
      v-if="pending"
      :title="confirmCopy().title"
      :body="confirmCopy().body"
      :action="confirmCopy().action"
      tone="error"
      :busy="busy"
      @cancel="pending = null"
      @confirm="confirmPending"
    />
  </div>
</template>
