<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import type { Component } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ApplicationWeb16,
  ChevronDown16,
  ChevronLeft16,
  ChevronRight16,
  ChevronUp16,
  Close16,
  Devices16,
  Edit16,
  Laptop16,
  Mobile16,
  Phone16,
  Renew16,
  Screen16,
  Terminal16,
  TrashCan16,
} from '@carbon/icons-vue'

import { useNoticeStore } from '@/stores/notice'
import { useSessionStore } from '@/stores/session'
import { sessionApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AuthSession, Device } from '@/lib/api/types'
import { deviceIdentity } from '@/lib/device'
import { describeAgent, place, relative, remaining, shortId, utcStamp } from '@/lib/format'
import { platformLabel, sessionTypeLabel } from '@/lib/enums'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import IconButton from '@/components/IconButton.vue'
import SecurityNav from '@/components/SecurityNav.vue'

const { t } = useI18n()
const notice = useNoticeStore()
const session = useSessionStore()

const devices = ref<Device[]>([])
const total = ref(0)
const loading = ref(true)
const error = ref('')
const busy = ref(false)
const page = ref(0)
const pageSize = 20
const expanded = ref<string[]>([])

/** The device this browser reports as itself; the server never infers it. */
const localDeviceId = deviceIdentity().id
const currentSessionId = computed(() => session.identity?.session_id)

const platformIcons: Record<number, Component> = {
  0: Devices16,
  1: ApplicationWeb16,
  2: Phone16,
  3: Mobile16,
  4: Laptop16,
  5: Screen16,
  6: Terminal16,
}

const platformIcon = (platform: number) => platformIcons[platform] ?? Devices16

/** The fleet's display rule: a non-empty label wins, else the declared name. */
function displayName(device: Device): string {
  const label = device.device_label?.trim()
  return label ? label : device.device_name
}

/** Sessions the server still considers live; revoked ones linger in the row. */
function liveSessions(device: Device): AuthSession[] {
  const now = Date.now()
  return device.sessions.filter(
    (entry) => !entry.expired_at || new Date(entry.expired_at).getTime() > now,
  )
}

function latestSession(device: Device): AuthSession | undefined {
  let latest: AuthSession | undefined
  for (const entry of liveSessions(device)) {
    const stamp = entry.last_granted_at ? new Date(entry.last_granted_at).getTime() : 0
    const latestStamp = latest?.last_granted_at ? new Date(latest.last_granted_at).getTime() : -1
    if (stamp > latestStamp) latest = entry
  }
  return latest
}

function lastActive(device: Device): string {
  return relative(latestSession(device)?.last_granted_at)
}

function origin(device: Device): { ip: string; place: string } {
  const latest = latestSession(device)
  return { ip: latest?.ip_address || '—', place: place(latest?.location) }
}

const isThisDevice = (device: Device) => device.device_id === localDeviceId

/**
 * A device row holding the session this console runs on cannot be revoked:
 * the request would sign the page out mid-flight. New logins bind the session
 * to the same device key, but older rows may differ, so both signals count.
 */
function holdsCurrentSession(device: Device): boolean {
  return device.sessions.some((entry) => entry.id === currentSessionId.value)
}

const selfDevice = (device: Device) => isThisDevice(device) || holdsCurrentSession(device)

const isExpanded = (device: Device) => expanded.value.includes(device.id)

function toggle(device: Device) {
  expanded.value = isExpanded(device)
    ? expanded.value.filter((id) => id !== device.id)
    : [...expanded.value, device.id]
}

type Pending =
  | { kind: 'device'; device: Device }
  | { kind: 'session'; device: Device; session: AuthSession }

const pending = ref<Pending | null>(null)
const renaming = ref<Device | null>(null)
const renameLabel = ref('')

const pendingCopy = computed(() => {
  const target = pending.value
  if (!target) return { title: '', body: '', action: '' }
  if (target.kind === 'device') {
    return {
      title: t('devices.revokeTitle'),
      body: t('devices.revokeBody', { device: displayName(target.device) }),
      action: t('devices.revokeAction'),
    }
  }
  return {
    title: t('devices.sessionRevokeTitle'),
    body: t('devices.sessionRevokeBody', {
      id: shortId(target.session.id),
      device: displayName(target.device),
    }),
    action: t('devices.sessionRevokeAction'),
  }
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const result = await sessionApi.devices({ take: pageSize, offset: page.value * pageSize })
    devices.value = result.items
    total.value = result.total
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('devices.errorLoad')
  } finally {
    loading.value = false
  }
}

function go(next: number) {
  page.value = Math.max(0, Math.min(next, Math.max(0, Math.ceil(total.value / pageSize) - 1)))
  void load()
}

function openRename(device: Device) {
  renaming.value = device
  renameLabel.value = device.device_label ?? device.device_name
}

async function saveRename() {
  const device = renaming.value
  const label = renameLabel.value.trim()
  if (!device || !label) return
  busy.value = true
  try {
    if (isThisDevice(device)) await sessionApi.renameCurrentDevice(label)
    else await sessionApi.renameDevice(device.device_id, label)
    notice.ok(t('devices.noticeRenamed'))
    renaming.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('devices.errorRename'))
  } finally {
    busy.value = false
  }
}

async function revoke() {
  const target = pending.value
  if (!target) return
  busy.value = true
  try {
    if (target.kind === 'device') {
      await sessionApi.deleteDevice(target.device.device_id)
      notice.ok(t('devices.noticeRevoked', { id: shortId(target.device.device_id) }))
    } else {
      await sessionApi.revoke(target.session.id)
      notice.ok(t('devices.noticeSessionRevoked', { id: shortId(target.session.id) }))
    }
    pending.value = null
    await load()
  } catch (caught) {
    notice.fail(
      caught instanceof Error
        ? caught.message
        : t(target.kind === 'device' ? 'devices.errorRevoke' : 'devices.errorSessionRevoke'),
    )
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader :title="t('devices.title')" :description="t('devices.description')">
      <template #actions>
        <IconButton :label="t('common.refresh')" :disabled="loading" @click="load">
          <Renew16 aria-hidden="true" />
        </IconButton>
      </template>
    </PageHeader>

    <SecurityNav />

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <ConsolePanel
      :title="t('devices.panelTitle')"
      :note="loading ? undefined : t('devices.note', { total })"
    >
      <LoadingSkeleton v-if="loading" variant="rows" :rows="4" />

      <p v-else-if="!devices.length" class="voice py-6">
        {{ t('devices.empty') }}
      </p>

      <div v-else class="table-bleed overflow-x-auto">
        <table class="ledger">
          <thead>
            <tr>
              <th scope="col">{{ t('devices.device') }}</th>
              <th scope="col">{{ t('devices.platform') }}</th>
              <th scope="col">{{ t('devices.sessions') }}</th>
              <th scope="col">{{ t('devices.lastActive') }}</th>
              <th scope="col">{{ t('devices.origin') }}</th>
              <th scope="col">
                <span class="sr-only">{{ t('common.actions') }}</span>
              </th>
            </tr>
          </thead>
          <tbody>
            <template v-for="device in devices" :key="device.id">
              <tr>
                <td>
                  <div class="flex items-center gap-2">
                    <component
                      :is="platformIcon(device.platform)"
                      class="h-4 w-4 shrink-0 text-faint"
                      aria-hidden="true"
                    />
                    <span class="text-xs text-base-content">{{ displayName(device) }}</span>
                    <span v-if="isThisDevice(device)" class="chip text-primary">
                      {{ t('devices.thisDevice') }}
                    </span>
                    <span
                      class="dot"
                      :class="device.is_online ? 'bg-success' : 'bg-line'"
                      :title="device.is_online ? t('common.online') : t('common.offline')"
                    />
                    <span v-if="device.trusted" class="chip text-accent">
                      {{ t('devices.trusted') }}
                    </span>
                  </div>
                  <p class="mt-1 text-2xs text-faint" :title="device.device_id">
                    {{ shortId(device.device_id) }}
                  </p>
                </td>
                <td class="text-xs text-dim">
                  {{ platformLabel(device.platform) }}
                </td>
                <td class="text-xs tabular-nums text-dim">
                  {{ liveSessions(device).length }}
                </td>
                <td class="text-xs text-dim">{{ lastActive(device) }}</td>
                <td class="text-xs text-dim">
                  <p>{{ origin(device).ip }}</p>
                  <p class="text-2xs text-faint">{{ origin(device).place }}</p>
                </td>
                <td>
                  <div class="flex items-center justify-end gap-1">
                    <IconButton
                      :label="isExpanded(device) ? t('devices.collapse') : t('devices.expand')"
                      :disabled="busy"
                      @click="toggle(device)"
                    >
                      <ChevronUp16 v-if="isExpanded(device)" aria-hidden="true" />
                      <ChevronDown16 v-else aria-hidden="true" />
                    </IconButton>
                    <IconButton
                      :label="t('devices.rename')"
                      :disabled="busy"
                      @click="openRename(device)"
                    >
                      <Edit16 aria-hidden="true" />
                    </IconButton>
                    <IconButton
                      v-if="!selfDevice(device)"
                      :label="t('devices.revoke')"
                      tone="danger"
                      :disabled="busy"
                      @click="pending = { kind: 'device', device }"
                    >
                      <TrashCan16 aria-hidden="true" />
                    </IconButton>
                    <span v-else class="text-2xs text-faint">—</span>
                  </div>
                </td>
              </tr>

              <tr v-if="isExpanded(device)">
                <td colspan="6">
                  <div class="text-left">
                    <p class="text-2xs uppercase tracking-[0.14em] text-faint">
                      {{ t('devices.boundSessions') }}
                    </p>
                    <p v-if="!liveSessions(device).length" class="voice mt-2 text-[12px]">
                      {{ t('devices.noSessions') }}
                    </p>
                    <ul v-else class="mt-2 space-y-3">
                      <li
                        v-for="entry in liveSessions(device)"
                        :key="entry.id"
                        class="flex flex-wrap items-center justify-between gap-3"
                      >
                        <div class="min-w-0">
                          <div class="flex flex-wrap items-center gap-2">
                            <span class="text-xs text-base-content">
                              {{ describeAgent(entry.user_agent) }}
                            </span>
                            <span class="chip">{{
                              sessionTypeLabel(entry.type) || t('devices.fallbackSessionType')
                            }}</span>
                            <span v-if="entry.id === currentSessionId" class="chip text-primary">
                              {{ t('devices.currentSession') }}
                            </span>
                            <span v-if="entry.trusted" class="chip text-accent">
                              {{ t('devices.trusted') }}
                            </span>
                          </div>
                          <p class="mt-1 text-2xs text-faint" :title="entry.id">
                            {{ shortId(entry.id) }} · {{ entry.ip_address || '—' }} ·
                            {{ place(entry.location) }} · {{ relative(entry.last_granted_at) }}
                          </p>
                        </div>
                        <div class="flex items-center gap-3">
                          <span class="text-2xs text-faint" :title="utcStamp(entry.expired_at)">
                            {{ remaining(entry.expired_at) }}
                          </span>
                          <IconButton
                            v-if="entry.id !== currentSessionId"
                            :label="t('devices.sessionRevoke')"
                            tone="danger"
                            :disabled="busy"
                            @click="pending = { kind: 'session', device, session: entry }"
                          >
                            <Close16 aria-hidden="true" />
                          </IconButton>
                        </div>
                      </li>
                    </ul>
                  </div>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>

      <template #footer>
        <div class="flex flex-wrap items-center justify-between gap-3 text-2xs text-faint">
          <span>{{ t('devices.footer') }}</span>
          <div class="flex items-center gap-3">
            <span v-if="!loading" class="tabular-nums">
              {{
                t('devices.page', {
                  page: page + 1,
                  pages: Math.max(1, Math.ceil(total / pageSize)),
                })
              }}
            </span>
            <button
              type="button"
              class="btn btn-ghost btn-xs px-1 text-dim disabled:text-faint"
              :aria-label="t('common.previousPage')"
              :title="t('common.previousPage')"
              :disabled="page === 0 || loading"
              @click="go(page - 1)"
            >
              <ChevronLeft16 aria-hidden="true" />
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-xs px-1 text-dim disabled:text-faint"
              :aria-label="t('common.nextPage')"
              :title="t('common.nextPage')"
              :disabled="page >= Math.ceil(total / pageSize) - 1 || loading"
              @click="go(page + 1)"
            >
              <ChevronRight16 aria-hidden="true" />
            </button>
          </div>
        </div>
      </template>
    </ConsolePanel>

    <ConfirmAction
      v-if="pending"
      :title="pendingCopy.title"
      :body="pendingCopy.body"
      :action="pendingCopy.action"
      tone="error"
      :busy="busy"
      @cancel="pending = null"
      @confirm="revoke"
    />

    <dialog
      v-if="renaming"
      class="modal modal-open"
      aria-modal="true"
      aria-labelledby="rename-device-title"
    >
      <div class="modal-box max-w-sm rounded-[2px] border border-line bg-base-100 p-0 shadow-none">
        <div class="border-b border-line px-5 py-4">
          <h2 id="rename-device-title" class="text-sm text-base-content">
            {{ t('devices.renameTitle') }}
          </h2>
        </div>
        <div class="px-5 py-4">
          <label for="device-label" class="field-label">{{ t('devices.label') }}</label>
          <input
            id="device-label"
            v-model="renameLabel"
            class="input input-sm"
            autocomplete="off"
            :placeholder="t('devices.labelPlaceholder')"
            :disabled="busy"
            @keydown.enter="saveRename"
          />
        </div>
        <div class="flex items-center justify-end gap-2 border-t border-line px-5 py-3">
          <button
            type="button"
            class="btn btn-ghost btn-xs"
            :disabled="busy"
            @click="renaming = null"
          >
            {{ t('common.cancel') }}
          </button>
          <button
            type="button"
            class="btn btn-primary btn-xs"
            :disabled="busy || !renameLabel.trim()"
            @click="saveRename"
          >
            {{ t('common.save') }}
          </button>
        </div>
      </div>
      <button
        type="button"
        class="modal-backdrop bg-black/60"
        :aria-label="t('common.cancel')"
        @click="renaming = null"
      />
    </dialog>
  </div>
</template>
