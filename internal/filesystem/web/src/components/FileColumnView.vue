<script setup lang="ts">
/**
 * Miller-column browser: each column lists every entry held by the folder
 * picked in the column to its left — subfolders and files together. The open
 * folder's contents are therefore the last column, not a separate panel, and
 * a column has no sheet of its own: only its row blocks carry tone.
 *
 * Selection is the checkbox's business; activating an entry opens a folder as
 * the next column or a file as a preview.
 */
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'

import FileGlyph from './FileGlyph.vue'
import type { CloudFile } from '@/lib/api/types'
import { displayName, formatBytes, formatCount } from '@/lib/format'
import { useFileOpener } from '@/lib/opening'
import { useVaultStore } from '@/stores/vault'

const vault = useVaultStore()
const route = useRoute()
const router = useRouter()
const opener = useFileOpener()
const { t } = useI18n()
const rootName = computed(() => vault.crumbs[0]?.name ?? t('nav.drive'))

/**
 * The folder a click has opened, at the column position it belongs to. It
 * shortens the trail the instant an entry is picked, so the columns to its
 * right disappear before the route has finished resolving; the watch clears
 * it once the breadcrumb catches up.
 */
const drill = ref<{ position: number; file: CloudFile } | null>(null)
const drillPath = ref('')
const trail = computed<CloudFile[]>(() => {
  const chain: CloudFile[] = [asFolder(null, rootName.value)]
  if (vault.mode === 'drive') {
    for (const crumb of vault.crumbs.slice(1)) chain.push(asFolder(crumb.id, crumb.name))
  }
  const target = drill.value
  if (!target) return chain
  if (chain[target.position]?.id === target.file.id) return chain
  return [...chain.slice(0, target.position), target.file]
})
const trailIds = computed(() => trail.value.map((entry) => entry.id))

/** Column entries carry a folder's own id, so the trail stays addressable. */
function asFolder(id: string | null, name: string): CloudFile {
  return {
    id: id ?? '',
    name,
    account_id: '',
    status: 0,
    indexed: true,
    is_folder: true,
    is_marked_recycle: false,
    created_at: '',
    updated_at: '',
  }
}

interface Column {
  /** The folder whose children this column lists; '' is the drive root. */
  parent: string
  /** Null while the column is still resolving. */
  rows: CloudFile[] | null
  failed: boolean
}

const listings = ref<Record<number, Column>>({})
const focused = ref<Record<number, string | undefined>>({})
const columnEls = ref<(HTMLElement | null)[]>([])
const loadSeq: Record<number, number> = {}

function parentIdAt(index: number): string | null {
  const folder = trail.value[index]
  if (index === 0 || !folder?.id) return null
  return folder.id
}

function parentKeyAt(index: number): string {
  return parentIdAt(index) ?? ''
}

function rowsAt(index: number): CloudFile[] {
  const column = listings.value[index]
  return column && column.parent === parentKeyAt(index) ? (column.rows ?? []) : []
}

/** True until this column's listing has arrived (or failed). */
function isResolving(index: number): boolean {
  const column = listings.value[index]
  return !column || column.parent !== parentKeyAt(index) || column.rows === null
}

/** A column whose listing failed offers a retry instead of an empty list. */
function failedAt(index: number): boolean {
  return listings.value[index]?.failed === true
}

/** The folder whose column is already showing. */
function isOpenPath(index: number, file: CloudFile): boolean {
  return file.is_folder && trailIds.value[index + 1] === file.id
}

function focusRow(index: number, row: number) {
  const entry = rowsAt(index)[row]
  if (!entry) return
  focused.value = { ...focused.value, [index]: entry.id }
  void nextTick(() => {
    const el = columnEls.value[index]?.querySelectorAll<HTMLElement>('[role="option"]')[row]
    el?.focus()
    el?.scrollIntoView({ block: 'nearest' })
  })
}

async function loadColumn(index: number) {
  const parent = parentKeyAt(index)
  const seq = (loadSeq[index] ?? 0) + 1
  loadSeq[index] = seq
  listings.value = { ...listings.value, [index]: { parent, rows: null, failed: false } }
  try {
    const rows = await vault.listChildren(parentIdAt(index))
    if (loadSeq[index] !== seq || parentKeyAt(index) !== parent) return
    listings.value = { ...listings.value, [index]: { parent, rows, failed: false } }
    const held = focused.value[index]
    if (!held || !rows.some((row) => row.id === held)) {
      focused.value = { ...focused.value, [index]: rows[0]?.id }
    }
  } catch {
    if (loadSeq[index] !== seq || parentKeyAt(index) !== parent) return
    listings.value = { ...listings.value, [index]: { parent, rows: [], failed: true } }
  }
}

/** Keeps the columns, the focused entry, and the route in step. */
watch(
  () => trailIds.value.join('/'),
  () => {
    const depth = trail.value.length
    const kept: Record<number, Column> = {}
    for (const [key, column] of Object.entries(listings.value)) {
      if (Number(key) < depth) kept[Number(key)] = column
    }
    listings.value = kept
    for (let index = 0; index < depth; index += 1) {
      if (isResolving(index)) void loadColumn(index)
    }
  },
  { immediate: true },
)

watch(
  () => route.path,
  (path) => {
    if (path !== drillPath.value) drill.value = null
  },
)

/** Descends into a folder: truncates the columns to its right, then opens it. */
function descend(index: number, file: CloudFile) {
  if (!file.is_folder) {
    opener.open(file)
    return
  }
  drill.value = { position: index + 1, file }
  drillPath.value = router.resolve({ name: 'folder', params: { id: file.id } }).fullPath
  focused.value = { ...focused.value, [index + 1]: undefined }
  opener.open(file)
}

function onKeydown(event: KeyboardEvent, index: number, row: number) {
  // A focused checkbox ticks itself on Space; toggling again would undo it.
  const onCheckbox = (event.target as HTMLElement | null)?.tagName === 'INPUT'
  if (onCheckbox && event.key === ' ') return
  const rows = rowsAt(index)
  if (event.key === 'ArrowDown') {
    event.preventDefault()
    if (row < rows.length - 1) focusRow(index, row + 1)
    return
  }
  if (event.key === 'ArrowUp') {
    event.preventDefault()
    if (row > 0) focusRow(index, row - 1)
    return
  }
  if (event.key === 'Home' || event.key === 'End') {
    event.preventDefault()
    focusRow(index, event.key === 'Home' ? 0 : rows.length - 1)
    return
  }
  const entry = rows[row]
  if (!entry) return
  if (event.key === 'ArrowRight') {
    event.preventDefault()
    if (!entry.is_folder) {
      opener.open(entry)
      return
    }
    const next = listings.value[index + 1]
    if (next?.parent === entry.id && (next.rows?.length ?? 0) > 0) {
      focusRow(index + 1, 0)
      return
    }
    descend(index, entry)
    return
  }
  if (event.key === 'ArrowLeft') {
    event.preventDefault()
    if (index === 0) return
    const parent = parentIdAt(index)
    const columnRows = rowsAt(index - 1)
    const at = columnRows.findIndex((candidate) => candidate.id === parent)
    if (at >= 0) focusRow(index - 1, at)
    return
  }
  if (event.key === 'Enter') {
    event.preventDefault()
    descend(index, entry)
    return
  }
  // Space ticks the checkbox, matching the checkbox semantics used elsewhere.
  if (event.key === ' ') {
    event.preventDefault()
    vault.toggle(entry.id)
  }
}

/** Registers a column's scroll container so arrow keys can move DOM focus. */
function setColumnEl(index: number, el: unknown) {
  columnEls.value[index] = el instanceof HTMLElement ? el : null
}

/** Folders report how much they hold, files report their bytes. */
function childMeta(file: CloudFile): string {
  if (file.is_folder) return formatCount(file.children_count ?? 0)
  return formatBytes(file.object?.size)
}

/** The first entry is the column's tab stop until the keyboard moves it. */
function isFocused(index: number, id: string): boolean {
  const held = focused.value[index]
  return held ? held === id : rowsAt(index)[0]?.id === id
}
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col">
    <div class="scroll-region flex min-h-0 flex-1 gap-2 overflow-x-auto p-2">
      <div v-for="(entry, index) in trail" :key="index" class="flex w-[260px] shrink-0 flex-col">
        <div class="flex h-10 shrink-0 items-center gap-2 px-3">
          <span class="eyebrow truncate" :title="entry.name">{{ entry.name }}</span>
          <span
            v-if="!isResolving(index)"
            class="ml-auto shrink-0 text-2xs tabular-nums text-faint"
          >
            {{ rowsAt(index).length }}
          </span>
        </div>

        <div
          :ref="(el) => setColumnEl(index, el)"
          class="scroll-region min-h-0 flex-1 overflow-y-auto px-1 pb-1"
        >
          <p v-if="isResolving(index)" class="voice px-2.5 py-2 text-[12px]">
            {{ t('status.reading') }}
          </p>
          <div v-else-if="failedAt(index)" class="px-2.5 py-2">
            <p class="voice text-[12px]">{{ t('error.loadFolderBody') }}</p>
            <button class="btn btn-ghost btn-sm mt-1" type="button" @click="loadColumn(index)">
              {{ t('error.tryAgain') }}
            </button>
          </div>
          <p v-else-if="rowsAt(index).length === 0" class="voice px-2.5 py-2 text-[12px]">
            {{ t('empty.nothingHere') }}
          </p>

          <div
            v-else
            role="listbox"
            class="flex flex-col gap-0.5"
            :aria-label="t('drive.objects')"
          >
            <div
              v-for="(child, row) in rowsAt(index)"
              :key="child.id"
              role="option"
              :aria-selected="vault.selection.includes(child.id)"
              :aria-current="isOpenPath(index, child) ? 'true' : undefined"
              :tabindex="isFocused(index, child.id) ? 0 : -1"
              class="flex h-8 cursor-default items-center gap-2 rounded-chip px-2 outline-offset-[-2px]"
              :class="
                isOpenPath(index, child)
                  ? 'bg-primary/12 font-semibold text-base-content'
                  : vault.selection.includes(child.id)
                    ? 'bg-primary/12 text-base-content'
                    : 'bg-base-200/75 text-dim hover:bg-base-300/70'
              "
              :title="displayName(child)"
              @click="descend(index, child)"
              @keydown="onKeydown($event, index, row)"
            >
              <FileGlyph :file="child" />
              <input
                type="checkbox"
                class="checkbox checkbox-xs shrink-0"
                :checked="vault.selection.includes(child.id)"
                :aria-label="t('drive.selectObject', { name: displayName(child) })"
                @change="vault.toggle(child.id)"
                @click.stop
              />
              <span class="min-w-0 flex-1 truncate text-[12.5px]">{{ displayName(child) }}</span>
              <span class="shrink-0 text-2xs tabular-nums text-faint">
                {{ childMeta(child) }}
              </span>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
