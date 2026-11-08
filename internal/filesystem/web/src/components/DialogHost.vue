<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Close16, View16, WarningAlt16 } from '@carbon/icons-vue'

import CopyButton from './CopyButton.vue'
import FolderPicker from './FolderPicker.vue'
import IconButton from './IconButton.vue'
import type { FilePermission } from '@/lib/api/types'
import { publicFileUrl } from '@/lib/api/drive'
import {
  formatBytes,
  formatCount,
  formatDateTime,
  kindLabel,
  mimeOf,
  shortHash,
} from '@/lib/format'
import { useFileOpener } from '@/lib/opening'
import { useSessionStore } from '@/stores/session'
import { useUiStore } from '@/stores/ui'
import { useVaultStore } from '@/stores/vault'

const ui = useUiStore()
const vault = useVaultStore()

const session = useSessionStore()
const opener = useFileOpener()

const dialog = computed(() => ui.dialog)

const { t } = useI18n()
const folderName = ref('')
const renameName = ref('')
const destination = ref<string | null>(null)
const folderInput = ref<HTMLInputElement | null>(null)
const renameInput = ref<HTMLInputElement | null>(null)

const moveFiles = computed(() => (dialog.value?.kind === 'move' ? dialog.value.files : []))
const renameFile = computed(() => (dialog.value?.kind === 'rename' ? dialog.value.file : null))
const confirmFiles = computed(() =>
  dialog.value?.kind === 'recycle' || dialog.value?.kind === 'purge' ? dialog.value.files : [],
)
const shareFile = computed(() => (dialog.value?.kind === 'share' ? dialog.value.file : null))
const shareIsPublic = computed(() => shareFile.value?.permission_status?.visibility === 'public')
const shareLink = computed(() =>
  shareFile.value ? `${window.location.origin}${publicFileUrl(shareFile.value.id)}` : '',
)

const inspectFile = computed(() => (dialog.value?.kind === 'inspect' ? dialog.value.file : null))
const permissionFile = computed(() =>
  dialog.value?.kind === 'permissions' ? dialog.value.file : null,
)
const permissionItems = ref<FilePermission[]>([])
const permissionLoading = ref(false)
const permissionError = ref('')
const newAccountId = ref('')
const newLevel = ref<'read' | 'write' | 'manage'>('read')
const grantError = ref('')
let permissionSeq = 0
const publicAccess = computed(() =>
  permissionItems.value.some((entry) => entry.subject_type === 'public'),
)
const grants = computed(() =>
  permissionItems.value.filter(
    (entry) => entry.subject_type !== 'public' && entry.subject_type !== 'private',
  ),
)
const inspectOwner = computed(() => {
  const owner = inspectFile.value?.account_id
  if (!owner) return ''
  return owner === session.identity?.id ? t('dialog.permissionsYou') : owner
})
const inspectAccess = computed(() =>
  inspectFile.value?.permission_status?.visibility === 'public'
    ? t('preview.public')
    : t('preview.private'),
)
const busy = computed(() => vault.actionBusy !== null)
const totalBytes = computed(() =>
  confirmFiles.value.reduce((sum, file) => sum + (file.object?.size ?? 0), 0),
)
const selectionLabel = computed(() => {
  const files = moveFiles.value
  if (files.length === 1) return files[0]?.name ?? t('dialog.object')
  return t('drive.manyItems', { n: formatCount(files.length) })
})
const confirmLabel = computed(() => {
  const files = confirmFiles.value
  if (files.length === 1) return files[0]?.name ?? t('dialog.object')
  return t('drive.manyItems', { n: formatCount(files.length) })
})
const confirmNoun = computed(() => {
  const files = confirmFiles.value
  const count = files.length
  const key = files.every((file) => file.is_folder) ? 'dialog.nounFolder' : 'dialog.nounObject'
  return t(key, { n: formatCount(count) }, count)
})
const sizeClause = computed(() =>
  totalBytes.value > 0 ? t('dialog.sizeClause', { size: formatBytes(totalBytes.value) }) : '',
)
const shortcutRows = computed<[string, string][]>(() => [
  [t('shortcuts.palette'), '⌘ K'],
  [t('shortcuts.dismiss'), 'Esc'],
  [t('shortcuts.newFolder'), 'N'],
  [t('shortcuts.upload'), 'U'],
  [t('shortcuts.recycle'), 'Del'],
  [t('shortcuts.open'), '↵'],
  [t('shortcuts.rangeSelect'), 'Shift click'],
  [t('shortcuts.toggleSelect'), '⌘ click'],
  [t('shortcuts.moveCursor'), '↑ ↓'],
])

watch(
  dialog,
  async (value) => {
    if (!value) {
      permissionSeq += 1
      return
    }
    await nextTick()
    if (value.kind === 'new-folder') {
      folderName.value = ''
      folderInput.value?.focus()
    } else if (value.kind === 'rename') {
      renameName.value = value.file.name
      renameInput.value?.focus()
      renameInput.value?.select()
    } else if (value.kind === 'move') {
      destination.value = vault.folderId
    } else if (value.kind === 'permissions') {
      const seq = ++permissionSeq
      permissionItems.value = []
      permissionError.value = ''
      permissionLoading.value = true
      newAccountId.value = ''
      newLevel.value = 'read'
      grantError.value = ''
      try {
        const items = await vault.permissionsFor(value.file)
        if (seq !== permissionSeq) return
        permissionItems.value = items
      } catch (caught) {
        if (seq !== permissionSeq) return
        permissionError.value =
          caught instanceof Error ? caught.message : t('dialog.permissionsLoadFailed')
      } finally {
        if (seq === permissionSeq) permissionLoading.value = false
      }
    }
  },
  { immediate: true },
)

function close() {
  if (busy.value) return
  ui.closeDialog()
}

function onDestination(payload: { id: string | null; path: string }) {
  destination.value = payload.id
}

async function submitFolder() {
  const name = folderName.value.trim()
  if (!name || busy.value) return
  try {
    await vault.createFolder(name)
    ui.closeDialog()
  } catch {
    // The store reports the failure and keeps the dialog open.
  }
}

async function submitRename() {
  const file = renameFile.value
  const name = renameName.value.trim()
  if (!file || !name || busy.value) return
  try {
    await vault.rename(file, name)
    ui.closeDialog()
  } catch {
    // Keep the dialog open.
  }
}

async function submitMove() {
  if (moveFiles.value.length === 0 || busy.value) return
  try {
    await vault.move(moveFiles.value, destination.value)
    ui.closeDialog()
  } catch {
    // Keep the dialog open.
  }
}

async function submitRecycle() {
  if (confirmFiles.value.length === 0 || busy.value) return
  try {
    await vault.recycle(confirmFiles.value)
    ui.closeDialog()
  } catch {
    // Keep the dialog open.
  }
}

async function submitPurge() {
  if (confirmFiles.value.length === 0 || busy.value) return
  try {
    await vault.purge(confirmFiles.value)
    ui.closeDialog()
  } catch {
    // Keep the dialog open.
  }
}

async function toggleShare() {
  if (!shareFile.value || busy.value) return
  try {
    await vault.setVisibility(shareFile.value, !shareIsPublic.value)
  } catch {
    // The store reports the failure.
  }
}

/** Flips the link marker in the staged rule set; nothing is sent until Save. */
function togglePublicAccess() {
  if (!permissionFile.value || busy.value) return
  const rest = permissionItems.value.filter(
    (entry) => entry.subject_type !== 'public' && entry.subject_type !== 'private',
  )
  permissionItems.value = [
    ...rest,
    {
      file_id: permissionFile.value.id,
      subject_type: publicAccess.value ? 'private' : 'public',
      subject_id: '',
      permission: 'read',
    },
  ]
}

function removeGrant(entry: FilePermission) {
  const index = permissionItems.value.indexOf(entry)
  if (index < 0) return
  const next = [...permissionItems.value]
  next.splice(index, 1)
  permissionItems.value = next
}

function addGrant() {
  const file = permissionFile.value
  const id = newAccountId.value.trim()
  if (!file) return
  if (!id) {
    grantError.value = t('dialog.permissionsInvalid')
    return
  }
  if (grants.value.some((entry) => entry.subject_type === 'account' && entry.subject_id === id)) {
    grantError.value = t('dialog.permissionsDuplicate')
    return
  }
  permissionItems.value = [
    ...permissionItems.value,
    {
      file_id: file.id,
      subject_type: 'account',
      subject_id: id,
      permission: newLevel.value,
    },
  ]
  newAccountId.value = ''
  grantError.value = ''
}

async function submitPermissions() {
  const file = permissionFile.value
  if (!file || busy.value || permissionLoading.value || permissionError.value) return
  const marker: FilePermission = {
    file_id: file.id,
    subject_type: publicAccess.value ? 'public' : 'private',
    subject_id: '',
    permission: 'read',
  }
  const rest = permissionItems.value.filter(
    (entry) => entry.subject_type !== 'public' && entry.subject_type !== 'private',
  )
  try {
    await vault.savePermissions(file, [...rest, marker])
    ui.closeDialog()
  } catch {
    // The store reports the failure and the dialog keeps the draft.
  }
}

function inspectPermissions() {
  if (!inspectFile.value) return
  ui.openDialog({ kind: 'permissions', file: inspectFile.value })
}

function openInspected() {
  if (!inspectFile.value) return
  const file = inspectFile.value
  ui.closeDialog()
  opener.open(file)
}
</script>

<template>
  <Teleport to="body">
    <Transition
      enter-active-class="transition duration-150 ease-out"
      enter-from-class="opacity-0"
      leave-active-class="transition duration-100 ease-in"
      leave-to-class="opacity-0"
    >
      <div
        v-if="dialog"
        class="fixed inset-0 z-[75] flex items-center justify-center overflow-y-auto bg-black/45 px-4 py-8 backdrop-blur-sm"
        @mousedown.self="close"
      >
        <div
          class="palette-in relative w-full rounded-sheet bg-base-200 shadow-[0_30px_80px_-40px_rgb(0_0_0/0.7)]"
          :class="dialog.kind === 'shortcuts' ? 'max-w-[540px]' : 'max-w-[440px]'"
          role="dialog"
          aria-modal="true"
          aria-labelledby="dialog-title"
        >
          <IconButton class="absolute right-2 top-2 z-10" :label="t('dialog.close')" @click="close">
            <Close16 />
          </IconButton>

          <template v-if="dialog.kind === 'new-folder'">
            <form @submit.prevent="submitFolder">
              <div class="px-5 pb-4 pt-5">
                <h2 id="dialog-title" class="title text-[18px] text-base-content">
                  {{ t('dialog.newFolder') }}
                </h2>
                <p class="voice mt-1">{{ t('dialog.newFolderNote') }}</p>
                <label class="field-label mt-4">{{ t('dialog.folderName') }}</label>
                <input
                  ref="folderInput"
                  v-model="folderName"
                  class="input"
                  type="text"
                  :placeholder="t('dialog.folderPlaceholder')"
                  autocomplete="off"
                  maxlength="160"
                />
              </div>
              <div class="flex justify-end gap-2 px-5 pb-5 pt-0">
                <button class="btn btn-ghost btn-sm" type="button" @click="close">
                  {{ t('dialog.cancel') }}
                </button>
                <button
                  class="btn btn-primary btn-sm"
                  type="submit"
                  :disabled="!folderName.trim() || busy"
                >
                  {{ busy ? t('dialog.creating') : t('dialog.create') }}
                </button>
              </div>
            </form>
          </template>

          <template v-else-if="dialog.kind === 'rename'">
            <form @submit.prevent="submitRename">
              <div class="px-5 pb-4 pt-5">
                <h2 id="dialog-title" class="title text-[18px] text-base-content">
                  {{ t('dialog.rename') }}
                </h2>
                <p class="voice mt-1">{{ t('dialog.renameNote') }}</p>
                <label class="field-label mt-4">{{ t('dialog.nameLabel') }}</label>
                <input
                  ref="renameInput"
                  v-model="renameName"
                  class="input"
                  type="text"
                  autocomplete="off"
                  maxlength="255"
                />
              </div>
              <div class="flex justify-end gap-2 px-5 pb-5 pt-0">
                <button class="btn btn-ghost btn-sm" type="button" @click="close">
                  {{ t('dialog.cancel') }}
                </button>
                <button
                  class="btn btn-primary btn-sm"
                  type="submit"
                  :disabled="!renameName.trim() || renameName.trim() === renameFile?.name || busy"
                >
                  {{ busy ? t('dialog.renaming') : t('dialog.renameAction') }}
                </button>
              </div>
            </form>
          </template>

          <template v-else-if="dialog.kind === 'move'">
            <div class="px-5 pb-3 pt-5">
              <h2 id="dialog-title" class="title text-[18px] text-base-content">
                {{ t('dialog.moveTitle', { selection: selectionLabel }) }}
              </h2>
              <p class="voice mt-1">{{ t('dialog.moveNote') }}</p>
            </div>
            <div class="px-5 pb-4">
              <FolderPicker
                :initial-id="vault.folderId"
                :exclude="moveFiles.map((file) => file.id)"
                @change="onDestination"
              />
            </div>
            <div class="flex justify-end gap-2 px-5 pb-5 pt-0">
              <button class="btn btn-ghost btn-sm" type="button" @click="close">
                {{ t('dialog.cancel') }}
              </button>
              <button
                class="btn btn-primary btn-sm"
                type="button"
                :disabled="busy"
                @click="submitMove"
              >
                {{ busy ? t('dialog.moving') : t('dialog.moveHere') }}
              </button>
            </div>
          </template>

          <template v-else-if="dialog.kind === 'recycle'">
            <div class="px-5 py-5">
              <h2 id="dialog-title" class="title text-[18px] text-base-content">
                {{ t('dialog.recycleTitle', { selection: confirmLabel }) }}
              </h2>
              <p class="voice mt-1.5">
                {{ t('dialog.recycleBody', { noun: confirmNoun, size: sizeClause }) }}
              </p>
              <div class="mt-3 rounded-chip bg-base-300/50 px-3 py-2 text-[11px] text-dim">
                <span class="text-faint">{{ t('dialog.recoveryLabel') }}</span> ·
                {{ t('dialog.recoveryNote') }}
              </div>
            </div>
            <div class="flex justify-end gap-2 px-5 pb-5 pt-0">
              <button class="btn btn-ghost btn-sm" type="button" @click="close">
                {{ t('dialog.cancel') }}
              </button>
              <button
                class="btn btn-primary btn-sm"
                type="button"
                :disabled="busy"
                @click="submitRecycle"
              >
                {{ busy ? t('dialog.recycling') : t('dialog.moveToRecycle') }}
              </button>
            </div>
          </template>

          <template v-else-if="dialog.kind === 'purge'">
            <div class="px-5 py-5">
              <h2 id="dialog-title" class="title text-[18px] text-error">
                {{ t('dialog.purgeTitle', { selection: confirmLabel }) }}
              </h2>
              <p class="voice mt-1.5">
                {{ t('dialog.purgeBody', { noun: confirmNoun, size: sizeClause }) }}
              </p>
              <div
                class="mt-3 flex items-center gap-2 rounded-chip bg-error/12 px-3 py-2 text-[11px] text-error"
              >
                <WarningAlt16 class="h-3.5 w-3.5 shrink-0" />
                {{ t('dialog.purgeWarning') }}
              </div>
            </div>
            <div class="flex justify-end gap-2 px-5 pb-5 pt-0">
              <button class="btn btn-ghost btn-sm" type="button" @click="close">
                {{ t('dialog.cancel') }}
              </button>
              <button
                class="btn btn-error btn-sm"
                type="button"
                :disabled="busy"
                @click="submitPurge"
              >
                {{ busy ? t('dialog.purging') : t('preview.deleteForever') }}
              </button>
            </div>
          </template>

          <template v-else-if="dialog.kind === 'share'">
            <div class="px-5 py-5 pr-12">
              <h2 id="dialog-title" class="title break-words text-[18px] text-base-content">
                {{ shareFile?.name }}
              </h2>
              <p class="voice mt-1">
                {{ t('dialog.shareNote') }}
              </p>

              <div
                class="mt-4 flex items-center justify-between gap-3 rounded-card bg-base-300/30 px-3.5 py-3"
              >
                <div>
                  <div class="text-[12px] text-base-content">{{ t('preview.publicLink') }}</div>
                  <div
                    class="mt-0.5 text-[11px]"
                    :class="shareIsPublic ? 'text-primary' : 'text-faint'"
                  >
                    {{ shareIsPublic ? t('dialog.shareOn') : t('dialog.shareOff') }}
                  </div>
                </div>
                <button
                  type="button"
                  role="switch"
                  :aria-checked="shareIsPublic"
                  class="relative h-6 w-11 shrink-0 rounded-full transition-colors duration-150 disabled:opacity-50"
                  :class="shareIsPublic ? 'bg-primary' : 'bg-base-300'"
                  :disabled="busy"
                  @click="toggleShare"
                >
                  <span
                    class="absolute top-[3px] size-[18px] rounded-full transition-[left] duration-150"
                    :class="
                      shareIsPublic ? 'left-[23px] bg-primary-content' : 'left-[3px] bg-faint'
                    "
                  />
                </button>
              </div>

              <div v-if="shareIsPublic" class="mt-2 flex items-stretch gap-2">
                <input
                  :value="shareLink"
                  readonly
                  class="input h-9 min-w-0 flex-1 px-3 font-mono text-2xs text-dim"
                  :aria-label="t('preview.publicLink')"
                  @focus="($event.target as HTMLInputElement).select()"
                />
                <CopyButton :value="shareLink" label="publicLink" />
              </div>
            </div>
            <div class="flex justify-end gap-2 px-5 pb-5 pt-0">
              <button class="btn btn-primary btn-sm" type="button" @click="close">
                {{ t('dialog.done') }}
              </button>
            </div>
          </template>

          <template v-else-if="dialog.kind === 'inspect'">
            <div class="px-5 py-5 pr-12">
              <h2 id="dialog-title" class="title break-words text-[18px] text-base-content">
                {{ inspectFile?.name }}
              </h2>
              <p class="voice mt-1">{{ t('dialog.inspectNote') }}</p>

              <dl
                v-if="inspectFile"
                class="mt-4 flex flex-col gap-0.5 rounded-card bg-base-300/30 p-1.5 text-[11px]"
              >
                <div class="flex items-start justify-between gap-3 rounded-chip px-2.5 py-1.5">
                  <dt class="shrink-0 text-faint">{{ t('dialog.nameLabel') }}</dt>
                  <dd class="min-w-0 break-all text-right text-dim">{{ inspectFile.name }}</dd>
                </div>
                <div class="flex items-start justify-between gap-3 rounded-chip px-2.5 py-1.5">
                  <dt class="shrink-0 text-faint">{{ t('dialog.inspectId') }}</dt>
                  <dd class="flex min-w-0 items-center justify-end gap-1.5">
                    <span class="min-w-0 truncate font-mono text-[10.5px] text-dim">
                      {{ inspectFile.id }}
                    </span>
                    <CopyButton :value="inspectFile.id" label="id" />
                  </dd>
                </div>
                <div class="flex items-start justify-between gap-3 rounded-chip px-2.5 py-1.5">
                  <dt class="shrink-0 text-faint">{{ t('meta.kind') }}</dt>
                  <dd class="text-right text-dim">{{ kindLabel(inspectFile) }}</dd>
                </div>
                <div class="flex items-start justify-between gap-3 rounded-chip px-2.5 py-1.5">
                  <dt class="shrink-0 text-faint">{{ t('meta.type') }}</dt>
                  <dd class="min-w-0 break-all text-right font-mono text-[10.5px] text-dim">
                    {{ mimeOf(inspectFile) }}
                  </dd>
                </div>
                <div class="flex items-start justify-between gap-3 rounded-chip px-2.5 py-1.5">
                  <dt class="shrink-0 text-faint">{{ t('meta.size') }}</dt>
                  <dd class="text-right tabular-nums text-dim">
                    {{ formatBytes(inspectFile.object?.size) }}
                  </dd>
                </div>
                <div class="flex items-start justify-between gap-3 rounded-chip px-2.5 py-1.5">
                  <dt class="shrink-0 text-faint">{{ t('meta.indexed') }}</dt>
                  <dd class="text-right text-dim">
                    {{ inspectFile.indexed ? t('meta.yes') : t('meta.no') }}
                  </dd>
                </div>
                <div class="flex items-start justify-between gap-3 rounded-chip px-2.5 py-1.5">
                  <dt class="shrink-0 text-faint">{{ t('dialog.inspectAccess') }}</dt>
                  <dd class="text-right text-dim">{{ inspectAccess }}</dd>
                </div>
                <div class="flex items-start justify-between gap-3 rounded-chip px-2.5 py-1.5">
                  <dt class="shrink-0 text-faint">{{ t('dialog.inspectOwner') }}</dt>
                  <dd class="min-w-0 break-all text-right font-mono text-[10.5px] text-dim">
                    {{ inspectOwner }}
                  </dd>
                </div>
                <div class="flex items-start justify-between gap-3 rounded-chip px-2.5 py-1.5">
                  <dt class="shrink-0 text-faint">{{ t('dialog.inspectParent') }}</dt>
                  <dd class="min-w-0 break-all text-right font-mono text-[10.5px] text-dim">
                    {{ inspectFile.parent_id ?? '—' }}
                  </dd>
                </div>
                <div class="flex items-start justify-between gap-3 rounded-chip px-2.5 py-1.5">
                  <dt class="shrink-0 text-faint">{{ t('meta.objectId') }}</dt>
                  <dd class="flex min-w-0 items-center justify-end gap-1.5">
                    <span class="min-w-0 truncate font-mono text-[10.5px] text-dim">
                      {{ inspectFile.object_id ?? '—' }}
                    </span>
                    <CopyButton
                      v-if="inspectFile.object_id"
                      :value="inspectFile.object_id"
                      label="id"
                    />
                  </dd>
                </div>
                <div
                  v-if="inspectFile.storage_key || inspectFile.object?.storage_key"
                  class="flex items-start justify-between gap-3 rounded-chip px-2.5 py-1.5"
                >
                  <dt class="shrink-0 text-faint">{{ t('dialog.inspectStorageKey') }}</dt>
                  <dd class="min-w-0 break-all text-right font-mono text-[10.5px] text-dim">
                    {{ inspectFile.storage_key ?? inspectFile.object?.storage_key }}
                  </dd>
                </div>
                <div
                  v-if="inspectFile.object?.hash"
                  class="flex items-start justify-between gap-3 rounded-chip px-2.5 py-1.5"
                >
                  <dt class="shrink-0 text-faint">{{ t('meta.hash') }}</dt>
                  <dd class="min-w-0 break-all text-right font-mono text-[10.5px] text-dim">
                    {{ shortHash(inspectFile.object.hash) }}
                  </dd>
                </div>
                <div class="flex items-start justify-between gap-3 rounded-chip px-2.5 py-1.5">
                  <dt class="shrink-0 text-faint">{{ t('meta.created') }}</dt>
                  <dd class="text-right tabular-nums text-dim">
                    {{ formatDateTime(inspectFile.created_at) }}
                  </dd>
                </div>
                <div class="flex items-start justify-between gap-3 rounded-chip px-2.5 py-1.5">
                  <dt class="shrink-0 text-faint">{{ t('meta.modified') }}</dt>
                  <dd class="text-right tabular-nums text-dim">
                    {{ formatDateTime(inspectFile.updated_at) }}
                  </dd>
                </div>
              </dl>
            </div>
            <div class="flex justify-end gap-2 px-5 pb-5 pt-0">
              <button class="btn btn-ghost btn-sm" type="button" @click="inspectPermissions">
                {{ t('menu.permissions') }}
              </button>
              <button class="btn btn-outline btn-sm" type="button" @click="openInspected">
                <View16 />
                {{ t('dialog.inspectOpen') }}
              </button>
              <button class="btn btn-primary btn-sm" type="button" @click="close">
                {{ t('dialog.close') }}
              </button>
            </div>
          </template>

          <template v-else-if="dialog.kind === 'permissions'">
            <div class="px-5 pb-4 pt-5 pr-12">
              <h2 id="dialog-title" class="title break-words text-[18px] text-base-content">
                {{ t('dialog.permissionsTitle') }}
              </h2>
              <p class="voice mt-1">
                {{
                  publicAccess
                    ? t('dialog.permissionsNotePublic', { name: permissionFile?.name ?? '' })
                    : t('dialog.permissionsNote', { name: permissionFile?.name ?? '' })
                }}
              </p>

              <div v-if="permissionLoading" class="mt-4 flex flex-col gap-1">
                <div
                  v-for="row in 3"
                  :key="row"
                  class="h-10 rounded-chip bg-base-300/40 pulse-soft"
                />
              </div>
              <div v-else-if="permissionError" class="mt-4 rounded-card bg-error/12 px-3.5 py-3">
                <p class="flex items-start gap-2 text-[11.5px] text-error">
                  <WarningAlt16 class="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
                  {{ permissionError }}
                </p>
                <p class="voice mt-1.5 text-[11px]">{{ t('dialog.permissionsLoadHint') }}</p>
              </div>
              <div v-else class="mt-4">
                <div
                  class="flex items-center justify-between gap-3 rounded-card bg-base-300/30 px-3.5 py-3"
                >
                  <div>
                    <div class="text-[12px] text-base-content">
                      {{ t('dialog.permissionsPublic') }}
                    </div>
                    <div
                      class="mt-0.5 text-[11px]"
                      :class="publicAccess ? 'text-primary' : 'text-faint'"
                    >
                      {{
                        publicAccess
                          ? t('dialog.permissionsPublicOn')
                          : t('dialog.permissionsPublicOff')
                      }}
                    </div>
                  </div>
                  <button
                    type="button"
                    role="switch"
                    :aria-checked="publicAccess"
                    class="relative h-6 w-11 shrink-0 rounded-full transition-colors duration-150 disabled:opacity-50"
                    :class="publicAccess ? 'bg-primary' : 'bg-base-300'"
                    :disabled="busy"
                    @click="togglePublicAccess"
                  >
                    <span
                      class="absolute top-[3px] size-[18px] rounded-full transition-[left] duration-150"
                      :class="
                        publicAccess ? 'left-[23px] bg-primary-content' : 'left-[3px] bg-faint'
                      "
                    />
                  </button>
                </div>

                <div class="mt-3 flex items-baseline justify-between gap-2 px-1">
                  <span class="text-[11.5px] text-base-content">{{
                    t('dialog.permissionsGrants')
                  }}</span>
                  <span v-if="grants.length" class="text-[10.5px] tabular-nums text-faint">
                    {{ formatCount(grants.length) }}
                  </span>
                </div>
                <ul v-if="grants.length" class="mt-1 flex flex-col gap-0.5">
                  <li
                    v-for="(entry, index) in grants"
                    :key="`${entry.subject_type}:${entry.subject_id}:${index}`"
                    class="flex items-center gap-2 rounded-chip bg-base-300/30 px-2.5 py-1.5 text-[11px]"
                  >
                    <span class="chip shrink-0">{{ entry.subject_type }}</span>
                    <code class="min-w-0 flex-1 truncate font-mono text-[10.5px] text-dim">
                      {{ entry.subject_id }}
                    </code>
                    <span class="chip shrink-0">{{ entry.permission }}</span>
                    <button
                      type="button"
                      class="shrink-0 rounded-chip p-1 text-faint transition-colors hover:bg-base-200 hover:text-error"
                      :aria-label="t('dialog.permissionsRemove')"
                      :title="t('dialog.permissionsRemove')"
                      :disabled="busy"
                      @click="removeGrant(entry)"
                    >
                      <Close16 class="h-3 w-3" />
                    </button>
                  </li>
                </ul>
                <p v-else class="voice mt-1 rounded-chip bg-base-300/20 px-2.5 py-2 text-[11px]">
                  {{ t('dialog.permissionsNoGrants') }}
                </p>

                <div class="mt-3 rounded-card bg-base-300/25 p-2.5">
                  <label class="field-label" for="grant-account">{{
                    t('dialog.permissionsAccount')
                  }}</label>
                  <div class="mt-1.5 flex items-stretch gap-1.5">
                    <input
                      id="grant-account"
                      v-model="newAccountId"
                      class="input h-9 min-w-0 flex-1 px-3 font-mono text-[11px]"
                      type="text"
                      autocomplete="off"
                      :placeholder="t('dialog.permissionsAccountPlaceholder')"
                      :disabled="busy"
                    />
                    <button
                      class="btn btn-outline btn-sm shrink-0"
                      type="button"
                      :disabled="busy"
                      @click="addGrant"
                    >
                      {{ t('dialog.permissionsAdd') }}
                    </button>
                  </div>
                  <div class="mt-2 flex items-center gap-2">
                    <span class="text-[11px] text-faint">{{ t('dialog.permissionsLevel') }}</span>
                    <div class="seg" role="group" :aria-label="t('dialog.permissionsLevel')">
                      <button
                        v-for="level in ['read', 'write', 'manage'] as const"
                        :key="level"
                        type="button"
                        class="seg-item"
                        :aria-selected="newLevel === level"
                        :disabled="busy"
                        @click="newLevel = level"
                      >
                        {{ t(`dialog.permissions_${level}`) }}
                      </button>
                    </div>
                  </div>
                  <p v-if="grantError" class="mt-1.5 text-[11px] text-error">{{ grantError }}</p>
                </div>
              </div>
            </div>
            <div class="flex justify-end gap-2 px-5 pb-5 pt-0">
              <button class="btn btn-ghost btn-sm" type="button" @click="close">
                {{ t('dialog.cancel') }}
              </button>
              <button
                class="btn btn-primary btn-sm"
                type="button"
                :disabled="permissionLoading || Boolean(permissionError) || busy"
                @click="submitPermissions"
              >
                {{ busy ? t('dialog.permissionsSaving') : t('dialog.permissionsSave') }}
              </button>
            </div>
          </template>
          <template v-else>
            <div class="px-5 py-5 pr-12">
              <h2 id="dialog-title" class="title text-[18px] text-base-content">
                {{ t('shortcuts.title') }}
              </h2>
              <p class="voice mt-1">{{ t('shortcuts.note') }}</p>
              <dl class="mt-4 flex flex-col gap-0.5 rounded-card bg-base-300/30 p-1.5 text-[11px]">
                <div
                  v-for="row in shortcutRows"
                  :key="row[0]"
                  class="flex items-center justify-between gap-4 rounded-chip px-2.5 py-2 transition-colors hover:bg-base-200/60"
                >
                  <dt class="text-dim">{{ row[0] }}</dt>
                  <dd class="flex shrink-0 gap-1">
                    <span v-for="key in row[1].split(' ')" :key="key" class="kbd">{{ key }}</span>
                  </dd>
                </div>
              </dl>
            </div>
            <div class="flex justify-end px-5 pb-5 pt-0">
              <button class="btn btn-primary btn-sm" type="button" @click="close">
                {{ t('dialog.close') }}
              </button>
            </div>
          </template>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>
