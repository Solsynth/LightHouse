<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { ActionLog, NameHistoryEntry, Punishment, Relationship } from '@/lib/api/types'
import { RELATIONSHIP_STATUS, punishmentTypeLabel } from '@/lib/enums'
import { relative, shortId, utcStamp } from '@/lib/format'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import TextField from '@/components/TextField.vue'

const props = defineProps<{ name: string }>()
const { t } = useI18n()
const notice = useNoticeStore()

const punishments = ref<Punishment[]>([])
const logs = ref<ActionLog[]>([])
const logsTotal = ref(0)
const nameHistory = ref<NameHistoryEntry[]>([])
const relationships = ref<Relationship[]>([])
const relationshipTotal = ref(0)
const loading = ref(true)
const busy = ref(false)
const error = ref('')

const createOpen = ref(false)
const createDraft = ref({
  type: 3,
  reason: '',
  expires: '',
  blocked: '',
})
const editing = ref<Punishment | null>(null)
const editDraft = ref({ type: 0, reason: '', expires: '', blocked: '' })
const pendingDelete = ref<Punishment | null>(null)
const logAction = ref('')

const relationshipStatus = (status: number) => {
  if (status === RELATIONSHIP_STATUS.closeFriend) return t('relationships.standing.closeFriend')
  if (status === RELATIONSHIP_STATUS.friends) return t('relationships.standing.friends')
  if (status === RELATIONSHIP_STATUS.blocked) return t('relationships.standing.blocked')
  if (status === RELATIONSHIP_STATUS.muted) return t('relationships.standing.muted')
  return t('relationships.standing.requested')
}

const activeCount = computed(() => punishments.value.filter((item) => !item.expired_at || new Date(item.expired_at) > new Date()).length)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [punishmentPage, logPage, history, relationshipPage] = await Promise.all([
      adminApi.accountPunishments(props.name, { take: 50 }),
      adminApi.accountActionLogs(props.name, { take: 50, action: logAction.value.trim() || undefined }),
      adminApi.accountNameHistory(props.name),
      adminApi.accountRelationships(props.name, { take: 50 }),
    ])
    punishments.value = punishmentPage.items
    logs.value = logPage.items
    logsTotal.value = logPage.total
    nameHistory.value = history
    relationships.value = relationshipPage.items
    relationshipTotal.value = relationshipPage.total
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('admin.account.moderation.errorLoad')
  } finally {
    loading.value = false
  }
}

function isoOrNull(value: string): string | null {
  if (!value) return null
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? null : date.toISOString()
}

function blockedList(value: string): string[] {
  return value
    .split(/[\s,]+/)
    .map((entry) => entry.trim())
    .filter(Boolean)
}

async function create() {
  busy.value = true
  try {
    await adminApi.createPunishment(props.name, {
      type: createDraft.value.type,
      reason: createDraft.value.reason.trim(),
      expired_at: isoOrNull(createDraft.value.expires),
      blocked_permissions: blockedList(createDraft.value.blocked),
    })
    notice.ok(t('admin.account.moderation.noticeCreated'))
    createOpen.value = false
    createDraft.value = { type: 3, reason: '', expires: '', blocked: '' }
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

function openEdit(item: Punishment) {
  editing.value = item
  editDraft.value = {
    type: item.type,
    reason: item.reason,
    expires: item.expired_at ? item.expired_at.slice(0, 16) : '',
    blocked: (item.blocked_permissions ?? []).join(', '),
  }
}

async function saveEdit() {
  const target = editing.value
  if (!target) return
  busy.value = true
  try {
    await adminApi.updatePunishment(props.name, target.id, {
      type: editDraft.value.type,
      reason: editDraft.value.reason.trim(),
      expired_at: isoOrNull(editDraft.value.expires),
      blocked_permissions: blockedList(editDraft.value.blocked),
    })
    notice.ok(t('admin.account.moderation.noticeUpdated'))
    editing.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function remove() {
  const target = pendingDelete.value
  if (!target) return
  busy.value = true
  try {
    await adminApi.deletePunishment(props.name, target.id)
    notice.ok(t('admin.account.moderation.noticeDeleted'))
    pendingDelete.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <p v-if="error" class="note note-error mb-4">{{ error }}</p>

    <LoadingSkeleton v-if="loading" :rows="4" :columns="[3, 2, 2, 1]" header />

    <template v-else>
      <ConsolePanel
        :title="t('admin.account.moderation.punishmentsTitle')"
        :note="t('admin.account.moderation.activeNote', { active: activeCount, total: punishments.length })"
      >
        <div class="mb-4 flex justify-end">
          <button type="button" class="btn btn-ghost btn-xs" :disabled="busy" @click="createOpen = true">
            {{ t('admin.account.moderation.create') }}
          </button>
        </div>

        <p v-if="!punishments.length" class="voice py-3 text-[12px]">
          {{ t('admin.account.moderation.noPunishments') }}
        </p>
        <ul v-else>
          <li
            v-for="item in punishments"
            :key="item.id"
            class="flex flex-wrap items-start justify-between gap-3 py-2 first:pt-0 last:pb-0"
          >
            <div class="min-w-0">
              <p class="text-xs text-base-content">
                {{ punishmentTypeLabel(item.type) }}
                <span
                  class="chip ml-1"
                  :class="
                    !item.expired_at || new Date(item.expired_at) > new Date()
                      ? 'text-error'
                      : 'text-faint'
                  "
                >
                  {{
                    !item.expired_at || new Date(item.expired_at) > new Date()
                      ? t('admin.account.moderation.active')
                      : t('admin.account.moderation.expired')
                  }}
                </span>
              </p>
              <p class="voice mt-1 text-[12px] text-dim">{{ item.reason }}</p>
              <p class="mt-0.5 text-2xs text-faint">
                {{ relative(item.created_at) }} ·
                {{ item.expired_at ? utcStamp(item.expired_at) : t('admin.account.moderation.never') }}
                <template v-if="item.blocked_permissions?.length">
                  · {{ item.blocked_permissions.join(', ') }}
                </template>
              </p>
            </div>
            <div class="flex items-center gap-1">
              <button type="button" class="btn btn-ghost btn-xs" :disabled="busy" @click="openEdit(item)">
                {{ t('common.edit') }}
              </button>
              <button
                type="button"
                class="btn btn-ghost btn-xs text-error"
                :disabled="busy"
                @click="pendingDelete = item"
              >
                {{ t('admin.account.identity.delete') }}
              </button>
            </div>
          </li>
        </ul>
      </ConsolePanel>

      <ConsolePanel
        :title="t('admin.account.moderation.logsTitle')"
        :note="t('admin.account.moderation.logsNote', { n: logsTotal })"
      >
        <form class="mb-4 flex flex-wrap items-end gap-2" @submit.prevent="load">
          <TextField
            id="account-log-action"
            v-model="logAction"
            root-class="min-w-[200px] flex-1"
            :label="t('admin.audit.logFilter')"
            :placeholder="t('admin.audit.logFilterPlaceholder')"
          />
          <button type="submit" class="btn btn-ghost btn-field" :disabled="busy">
            {{ t('common.search') }}
          </button>
        </form>

        <p v-if="!logs.length" class="voice py-3 text-[12px]">
          {{ t('admin.audit.logsEmpty') }}
        </p>
        <ul v-else class="divide-y divide-line-soft">
          <li
            v-for="log in logs"
            :key="log.id"
            class="flex flex-wrap items-baseline justify-between gap-2 py-2 first:pt-0 last:pb-0"
          >
            <div class="min-w-0">
              <p class="font-mono text-2xs text-base-content">{{ log.action }}</p>
              <p class="mt-0.5 truncate text-2xs text-faint">
                {{ log.ip_address || t('admin.audit.noAddress') }}
                <template v-if="log.session_id"> · {{ shortId(log.session_id) }}</template>
              </p>
            </div>
            <span class="text-2xs text-faint" :title="utcStamp(log.created_at)">
              {{ relative(log.created_at) }}
            </span>
          </li>
        </ul>
      </ConsolePanel>

      <ConsolePanel :title="t('admin.account.moderation.historyTitle')" :note="`${nameHistory.length}`">
        <p v-if="!nameHistory.length" class="voice py-3 text-[12px]">
          {{ t('admin.account.moderation.noHistory') }}
        </p>
        <ul v-else class="divide-y divide-line-soft">
          <li
            v-for="entry in nameHistory"
            :key="entry.id"
            class="flex items-center justify-between gap-2 py-2 first:pt-0 last:pb-0"
          >
            <span class="font-mono text-xs text-dim">{{ entry.name }}</span>
            <span class="text-2xs text-faint">{{ relative(entry.created_at) }}</span>
          </li>
        </ul>
      </ConsolePanel>

      <ConsolePanel
        :title="t('admin.account.moderation.relationshipsTitle')"
        :note="`${relationshipTotal}`"
      >
        <p v-if="!relationships.length" class="voice py-3 text-[12px]">
          {{ t('admin.account.moderation.noRelationships') }}
        </p>
        <ul v-else class="divide-y divide-line-soft">
          <li
            v-for="row in relationships"
            :key="`${row.account_id}-${row.related_id}`"
            class="flex flex-wrap items-center justify-between gap-2 py-2 first:pt-0 last:pb-0"
          >
            <div class="min-w-0">
              <RouterLink
                v-if="row.account"
                :to="`/admin/accounts/${encodeURIComponent(row.account.name)}`"
                class="text-xs text-base-content hover:underline"
              >
                {{ row.account.nick || row.account.name }}
              </RouterLink>
              <span v-else class="font-mono text-2xs text-faint">{{ shortId(row.account_id) }}</span>
              <span class="mx-1 text-2xs text-faint">→</span>
              <RouterLink
                v-if="row.related"
                :to="`/admin/accounts/${encodeURIComponent(row.related.name)}`"
                class="text-xs text-base-content hover:underline"
              >
                {{ row.related.nick || row.related.name }}
              </RouterLink>
              <span v-else class="font-mono text-2xs text-faint">{{ shortId(row.related_id) }}</span>
            </div>
            <div class="flex items-center gap-2">
              <span class="chip">{{ relationshipStatus(row.status) }}</span>
              <span class="text-2xs text-faint">{{ relative(row.created_at) }}</span>
            </div>
          </li>
        </ul>
      </ConsolePanel>
    </template>

    <dialog v-if="createOpen" class="modal modal-open" aria-modal="true" aria-labelledby="punishment-create-title">
      <div class="modal-box max-w-md overflow-hidden p-0">
        <div class="bg-base-200/40 px-6 py-5">
          <h2 id="punishment-create-title" class="text-sm text-base-content">
            {{ t('admin.account.moderation.create') }}
          </h2>
        </div>
        <form class="space-y-4 px-6 py-5" @submit.prevent="create">
          <TextField id="punishment-type" v-model.number="createDraft.type" type="number" :label="t('admin.account.moderation.type')" />
          <TextField
            id="punishment-reason"
            v-model="createDraft.reason"
            as="textarea"
            :rows="3"
            :label="t('admin.account.moderation.reason')"
          />
          <TextField
            id="punishment-expires"
            v-model="createDraft.expires"
            type="datetime-local"
            :label="t('common.expires')"
          />
          <TextField
            id="punishment-blocked"
            v-model="createDraft.blocked"
            :label="t('admin.account.moderation.blocked')"
            :hint="t('admin.account.moderation.blockedHint')"
          />
          <div class="flex items-center justify-end gap-2">
            <button type="button" class="btn btn-ghost btn-xs" @click="createOpen = false">
              {{ t('common.cancel') }}
            </button>
            <button
              type="submit"
              class="btn btn-error btn-xs"
              :disabled="busy || !createDraft.reason.trim()"
            >
              {{ t('admin.account.moderation.create') }}
            </button>
          </div>
        </form>
      </div>
      <button type="button" class="modal-backdrop bg-black/60" :aria-label="t('common.cancel')" @click="createOpen = false" />
    </dialog>

    <dialog v-if="editing" class="modal modal-open" aria-modal="true" aria-labelledby="punishment-edit-title">
      <div class="modal-box max-w-md overflow-hidden p-0">
        <div class="bg-base-200/40 px-6 py-5">
          <h2 id="punishment-edit-title" class="text-sm text-base-content">
            {{ t('admin.account.moderation.edit') }}
          </h2>
        </div>
        <form class="space-y-4 px-6 py-5" @submit.prevent="saveEdit">
          <TextField id="punishment-edit-type" v-model.number="editDraft.type" type="number" :label="t('admin.account.moderation.type')" />
          <TextField
            id="punishment-edit-reason"
            v-model="editDraft.reason"
            as="textarea"
            :rows="3"
            :label="t('admin.account.moderation.reason')"
          />
          <TextField
            id="punishment-edit-expires"
            v-model="editDraft.expires"
            type="datetime-local"
            :label="t('common.expires')"
          />
          <TextField
            id="punishment-edit-blocked"
            v-model="editDraft.blocked"
            :label="t('admin.account.moderation.blocked')"
          />
          <div class="flex items-center justify-end gap-2">
            <button type="button" class="btn btn-ghost btn-xs" @click="editing = null">
              {{ t('common.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary btn-xs" :disabled="busy">
              {{ t('common.save') }}
            </button>
          </div>
        </form>
      </div>
      <button type="button" class="modal-backdrop bg-black/60" :aria-label="t('common.cancel')" @click="editing = null" />
    </dialog>

    <ConfirmAction
      v-if="pendingDelete"
      :title="t('admin.account.moderation.confirmDeleteTitle')"
      :body="t('admin.account.moderation.confirmDeleteBody')"
      :action="t('admin.account.identity.delete')"
      tone="error"
      :busy="busy"
      @cancel="pendingDelete = null"
      @confirm="remove"
    />
  </div>
</template>
