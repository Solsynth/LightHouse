<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Edit16, TrashCan16 } from '@carbon/icons-vue'

import { useNoticeStore } from '@/stores/notice'
import { keysApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { PGPKey, SSHKey } from '@/lib/api/types'
import { relative } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import IconButton from '@/components/IconButton.vue'
import SecurityNav from '@/components/SecurityNav.vue'
import TextField from '@/components/TextField.vue'

type KeyKind = 'ssh' | 'pgp'

const { t } = useI18n()
const notice = useNoticeStore()

const sshKeys = ref<SSHKey[]>([])
const pgpKeys = ref<PGPKey[]>([])
const loading = ref(true)
const error = ref('')
const busy = ref(false)
const adding = ref<KeyKind | null>(null)
const addLabel = ref('')
const addValue = ref('')
const renaming = ref<{ kind: KeyKind; id: string; label: string } | null>(null)
const pendingDelete = ref<{ kind: KeyKind; id: string; label: string } | null>(null)

const canAdd = computed(() => addValue.value.trim().length > 0)
const addingTitle = computed(() =>
  adding.value === 'pgp' ? t('keys.pgpKind') : t('keys.sshKind'),
)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [ssh, pgp] = await Promise.all([keysApi.ssh(), keysApi.pgp()])
    sshKeys.value = ssh ?? []
    pgpKeys.value = pgp ?? []
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('keys.errorLoad')
  } finally {
    loading.value = false
  }
}

function openAdd(kind: KeyKind) {
  adding.value = kind
  addLabel.value = ''
  addValue.value = ''
}

async function addKey() {
  if (!adding.value || !canAdd.value) return
  busy.value = true
  try {
    const label = addLabel.value.trim()
    if (adding.value === 'ssh') {
      await keysApi.addSSH({ label: label || undefined, public_key: addValue.value })
    } else {
      await keysApi.addPGP({ label: label || undefined, public_key: addValue.value })
    }
    notice.ok(t('keys.noticeAdded'))
    adding.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof StargateError ? caught.message : t('keys.errorAdd'))
  } finally {
    busy.value = false
  }
}

function openRename(kind: KeyKind, key: { id: string; label: string }) {
  renaming.value = { kind, id: key.id, label: key.label }
}

async function saveRename() {
  const target = renaming.value
  const label = target?.label.trim()
  if (!target || !label) return
  busy.value = true
  try {
    if (target.kind === 'ssh') {
      await keysApi.renameSSH(target.id, label)
    } else {
      await keysApi.renamePGP(target.id, label)
    }
    notice.ok(t('keys.noticeRenamed'))
    renaming.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof StargateError ? caught.message : t('keys.errorRename'))
  } finally {
    busy.value = false
  }
}

async function remove() {
  const target = pendingDelete.value
  if (!target) return
  busy.value = true
  try {
    if (target.kind === 'ssh') {
      await keysApi.deleteSSH(target.id)
    } else {
      await keysApi.deletePGP(target.id)
    }
    notice.ok(t('keys.noticeDeleted'))
    pendingDelete.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof StargateError ? caught.message : t('keys.errorDelete'))
  } finally {
    busy.value = false
  }
}

function keyName(kind: KeyKind, key: { label: string }): string {
  return key.label || t('keys.unnamed', { kind: kind === 'ssh' ? t('keys.sshKind') : t('keys.pgpKind') })
}

function countNote(n: number): string {
  return n === 1 ? t('keys.noteOne') : t('keys.note', { n })
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader :title="t('keys.title')" :description="t('keys.description')">
      <template #actions>
        <button
          type="button"
          class="btn btn-ghost btn-sm"
          :disabled="loading"
          @click="openAdd('pgp')"
        >
          {{ t('keys.addPgp') }}
        </button>
        <button
          type="button"
          class="btn btn-primary btn-sm"
          :disabled="loading"
          @click="openAdd('ssh')"
        >
          {{ t('keys.addSsh') }}
        </button>
      </template>
    </PageHeader>

    <SecurityNav />

    <p v-if="error" class="note note-error mb-4">{{ error }}</p>

    <ConsolePanel
      :title="t('keys.sshPanel')"
      :note="loading ? undefined : countNote(sshKeys.length)"
    >
      <LoadingSkeleton v-if="loading" variant="rows" :rows="2" />
      <p v-else-if="!sshKeys.length" class="voice py-4 text-[12px]">
        {{ t('keys.emptySsh') }}
      </p>
      <ul v-else>
        <li
          v-for="key in sshKeys"
          :key="key.id"
          class="flex flex-wrap items-start justify-between gap-3 py-3 first:pt-0 last:pb-0"
        >
          <div class="min-w-0 flex-1">
            <p class="text-xs text-base-content">{{ keyName('ssh', key) }}</p>
            <p class="mt-1 text-2xs text-faint">
              {{ key.algorithm }} · {{ t('keys.added', { time: relative(key.created_at) }) }}
            </p>
            <p class="mt-1 break-all font-mono text-2xs text-faint">{{ key.fingerprint }}</p>
          </div>
          <div class="flex items-center gap-1">
            <IconButton :label="t('keys.rename')" :disabled="busy" @click="openRename('ssh', key)">
              <Edit16 aria-hidden="true" />
            </IconButton>
            <IconButton
              :label="t('common.delete')"
              tone="danger"
              :disabled="busy"
              @click="pendingDelete = { kind: 'ssh', id: key.id, label: keyName('ssh', key) }"
            >
              <TrashCan16 aria-hidden="true" />
            </IconButton>
          </div>
        </li>
      </ul>
    </ConsolePanel>

    <ConsolePanel
      class="mt-5"
      :title="t('keys.pgpPanel')"
      :note="loading ? undefined : countNote(pgpKeys.length)"
    >
      <LoadingSkeleton v-if="loading" variant="rows" :rows="2" />
      <p v-else-if="!pgpKeys.length" class="voice py-4 text-[12px]">
        {{ t('keys.emptyPgp') }}
      </p>
      <ul v-else>
        <li
          v-for="key in pgpKeys"
          :key="key.id"
          class="flex flex-wrap items-start justify-between gap-3 py-3 first:pt-0 last:pb-0"
        >
          <div class="min-w-0 flex-1">
            <p class="text-xs text-base-content">{{ keyName('pgp', key) }}</p>
            <p class="mt-1 text-2xs text-faint">
              {{ key.algorithm }} · {{ key.key_id }} ·
              {{ t('keys.added', { time: relative(key.created_at) }) }}
            </p>
            <p
              v-if="key.user_ids.length"
              class="mt-1 truncate text-2xs text-faint"
              :title="key.user_ids.join(', ')"
            >
              {{ key.user_ids.join(' · ') }}
            </p>
            <p class="mt-1 break-all font-mono text-2xs text-faint">{{ key.fingerprint }}</p>
          </div>
          <div class="flex items-center gap-1">
            <IconButton :label="t('keys.rename')" :disabled="busy" @click="openRename('pgp', key)">
              <Edit16 aria-hidden="true" />
            </IconButton>
            <IconButton
              :label="t('common.delete')"
              tone="danger"
              :disabled="busy"
              @click="pendingDelete = { kind: 'pgp', id: key.id, label: keyName('pgp', key) }"
            >
              <TrashCan16 aria-hidden="true" />
            </IconButton>
          </div>
        </li>
      </ul>
    </ConsolePanel>

    <ConfirmAction
      v-if="pendingDelete"
      :title="t('keys.deleteTitle')"
      :body="t('keys.deleteBody', { label: pendingDelete.label })"
      :action="t('keys.deleteAction')"
      tone="error"
      :busy="busy"
      @cancel="pendingDelete = null"
      @confirm="remove"
    />

    <dialog
      v-if="adding"
      class="modal modal-open"
      aria-modal="true"
      aria-labelledby="add-key-title"
    >
      <div class="modal-box max-w-md overflow-hidden p-0">
        <div class="bg-base-200/40 px-6 py-5">
          <h2 id="add-key-title" class="text-sm text-base-content">
            {{ t('keys.addTitle', { kind: addingTitle }) }}
          </h2>
        </div>
        <div class="space-y-4 px-6 py-5">
          <TextField
            id="key-label"
            v-model="addLabel"
            :label="t('keys.label')"
            autocomplete="off"
            :disabled="busy"
          />
          <TextField
            id="key-value"
            v-model="addValue"
            as="textarea"
            :rows="8"
            :label="t('keys.publicKey')"
            :hint="adding === 'ssh' ? t('keys.sshHint') : t('keys.pgpHint')"
            control-class="font-mono text-2xs"
            :disabled="busy"
          />
        </div>
        <div class="flex items-center justify-end gap-2 bg-base-200/50 px-5 py-3">
          <button type="button" class="btn btn-ghost btn-xs" :disabled="busy" @click="adding = null">
            {{ t('common.cancel') }}
          </button>
          <button
            type="button"
            class="btn btn-primary btn-xs"
            :disabled="busy || !canAdd"
            @click="addKey"
          >
            {{ busy ? t('keys.adding') : t('keys.add') }}
          </button>
        </div>
      </div>
      <button
        type="button"
        class="modal-backdrop bg-black/60"
        :aria-label="t('common.cancel')"
        @click="adding = null"
      />
    </dialog>

    <dialog
      v-if="renaming"
      class="modal modal-open"
      aria-modal="true"
      aria-labelledby="rename-key-title"
    >
      <div class="modal-box max-w-sm overflow-hidden p-0">
        <div class="bg-base-200/40 px-6 py-5">
          <h2 id="rename-key-title" class="text-sm text-base-content">
            {{ t('keys.renameTitle') }}
          </h2>
        </div>
        <div class="px-6 py-5">
          <TextField
            id="rename-key-label"
            v-model="renaming.label"
            :label="t('keys.label')"
            autocomplete="off"
            :disabled="busy"
            @keydown.enter="saveRename"
          />
        </div>
        <div class="flex items-center justify-end gap-2 bg-base-200/50 px-5 py-3">
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
            :disabled="busy || !renaming.label.trim()"
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
