<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import {
  ArrowUp16,
  Catalog16,
  Checkmark16,
  ChevronDown16,
  Close16,
  FolderAdd16,
  Grid16,
  List16,
  Move16,
  Renew16,
  TableSplit16,
  TrashCan16,
  Undo16,
} from '@carbon/icons-vue'
import type { Component } from 'vue'

import type { FileOrder, KindFilter, ViewMode } from '@/lib/api/types'
import { formatBytes, formatCount } from '@/lib/format'
import IconButton from './IconButton.vue'
import { useUiStore } from '@/stores/ui'
import { useVaultStore } from '@/stores/vault'

const vault = useVaultStore()
const ui = useUiStore()
const route = useRoute()
const router = useRouter()
const { t } = useI18n()

const mode = computed(() => route.meta.mode ?? 'drive')
const onDrive = computed(() => mode.value === 'drive')

/** The four browsing surfaces: rows, tiles, Miller columns, and a waterfall. */
const views: { key: ViewMode; icon: Component }[] = [
  { key: 'list', icon: List16 },
  { key: 'grid', icon: Grid16 },
  { key: 'column', icon: TableSplit16 },
  { key: 'waterfall', icon: Catalog16 },
]

const availableViews = computed(() =>
  mode.value === 'unindexed' ? views.filter((entry) => entry.key !== 'column') : views,
)

/** Kind and sort chips carry catalog keys; view chips are titled separately. */
const kinds: { key: KindFilter; label: string }[] = [
  { key: 'all', label: 'drive.kindAll' },
  { key: 'folders', label: 'drive.kindFolders' },
  { key: 'files', label: 'drive.kindFiles' },
]

const sorts: { key: FileOrder; label: string }[] = [
  { key: 'date', label: 'drive.sortDate' },
  { key: 'name', label: 'drive.sortName' },
  { key: 'size', label: 'drive.sortSize' },
]

const currentSort = computed(() => sorts.find((entry) => entry.key === vault.order) ?? sorts[0]!)
const currentKind = computed(() => kinds.find((entry) => entry.key === vault.kind) ?? kinds[0]!)
const sortArrow = computed(() => (vault.orderDesc ? '↓' : '↑'))

function closeMenu(event: Event) {
  const trigger = event.currentTarget as HTMLElement | null
  trigger?.closest('details')?.removeAttribute('open')
}

function openMenu(event: Event) {
  const current = (event.currentTarget as HTMLElement).closest('details')
  if (!(current instanceof HTMLDetailsElement)) return
  document.querySelectorAll<HTMLDetailsElement>('details[data-toolbar-menu]').forEach((entry) => {
    if (entry !== current) entry.open = false
  })
}

function chooseSort(key: FileOrder, event: Event) {
  sortBy(key)
  closeMenu(event)
}

function chooseKind(key: KindFilter, event: Event) {
  vault.setKind(key)
  closeMenu(event)
}

/** The view buttons name the mode they switch to; the union keeps the key real. */
function viewLabel(key: ViewMode): string {
  return t(`view.${key}`)
}

const hasSelection = computed(() => vault.selectedItems.length > 0)
const selectionCount = computed(() => vault.selectedItems.length)


function crumbLabel(crumb: { id: string | null; name: string }): string {
  if (crumb.id !== null) return crumb.name
  if (mode.value === 'recycle') return t('nav.recycle')
  if (mode.value === 'unindexed') return t('nav.unindexed')
  return t('nav.drive')
}

function up() {
  if (!onDrive.value) return
  const parent = vault.folder?.parent_id ?? null
  if (parent) void router.push({ name: 'folder', params: { id: parent } })
  else void router.push({ name: 'drive' })
}

function goCrumb(id: string | null) {
  if (id) void router.push({ name: 'folder', params: { id } })
  else void router.push({ name: 'drive' })
}

function sortBy(key: FileOrder) {
  vault.setOrder(key)
}

function newFolder() {
  ui.openDialog({ kind: 'new-folder' })
}

function refresh() {
  void vault.refreshAll()
}

function moveSelection() {
  ui.openDialog({ kind: 'move', files: [...vault.selectedItems] })
}

function recycleSelection() {
  ui.openDialog({ kind: 'recycle', files: [...vault.selectedItems] })
}

function purgeSelection() {
  ui.openDialog({ kind: 'purge', files: [...vault.selectedItems] })
}

function restoreSelection() {
  void vault.restore([...vault.selectedItems]).catch(() => undefined)
}
</script>

<template>
  <div class="shrink-0 px-3 pb-2 pt-2.5 md:px-4">
    <div class="flex min-h-[40px] flex-wrap items-center gap-x-2 gap-y-1">
      <IconButton v-if="onDrive && vault.folder" :label="t('drive.up')" @click="up">
        <ArrowUp16 />
      </IconButton>

      <nav
        class="flex min-w-0 items-center gap-0.5 overflow-hidden text-[12.5px]"
        :aria-label="t('drive.breadcrumb')"
      >
        <template v-for="(crumb, index) in vault.crumbs" :key="crumb.id ?? 'root'">
          <span v-if="index > 0" class="shrink-0 px-0.5 text-faint/60" aria-hidden="true">/</span>
          <button
            v-if="index < vault.crumbs.length - 1"
            type="button"
            class="max-w-[160px] shrink-0 truncate rounded-[12px] px-2 py-1 text-dim transition-colors hover:bg-base-200/70 hover:text-base-content"
            @click="goCrumb(crumb.id)"
          >
            {{ crumbLabel(crumb) }}
          </button>
          <span
            v-else
            class="max-w-[220px] truncate rounded-[12px] px-2 py-1 font-bold text-base-content sm:max-w-[320px]"
            aria-current="page"
          >
            {{ crumbLabel(crumb) }}
          </span>
        </template>
      </nav>

      <span
        v-if="vault.activePool"
        class="chip ml-1 pl-2.5 pr-1"
        :title="t('drive.filteredTo', { pool: vault.activePool.name })"
      >
        {{ vault.activePool.name }}
        <button
          type="button"
          class="grid size-4 place-items-center rounded-full text-faint transition-colors hover:bg-base-300 hover:text-base-content"
          :aria-label="t('drive.clearPoolFilter')"
          :title="t('drive.clearPoolFilter')"
          @click="vault.setPoolFilter(null)"
        >
          <Close16 class="h-3 w-3" />
        </button>
      </span>

      <span v-if="vault.query.trim()" class="chip ml-1 pl-2.5 pr-1">
        <span class="max-w-[140px] truncate">{{ vault.query.trim() }}</span>
        <button
          type="button"
          class="grid size-4 place-items-center rounded-full text-faint transition-colors hover:bg-base-300 hover:text-base-content"
          :aria-label="t('drive.clearSearch')"
          :title="t('drive.clearSearch')"
          @click="vault.setQuery('')"
        >
          <Close16 class="h-3 w-3" />
        </button>
      </span>

      <div class="ml-auto flex flex-wrap items-center justify-end gap-1.5">
        <IconButton
          :label="vault.loading ? t('drive.refreshing') : t('drive.refresh')"
          :disabled="vault.loading"
          @click="refresh"
        >
          <Renew16 :class="vault.loading ? 'animate-spin' : ''" />
        </IconButton>
        <IconButton v-if="onDrive" :label="t('drive.newFolder')" @click="newFolder">
          <FolderAdd16 />
        </IconButton>
        <button
          v-else-if="mode === 'recycle' && vault.total > 0"
          class="btn btn-ghost btn-xs"
          type="button"
          @click="vault.selectAll()"
        >
          {{ t('drive.selectAll') }}
        </button>

        <details data-toolbar-menu class="dropdown dropdown-end">
          <summary
            class="menu-trigger"
            :title="`${t('drive.sort')}: ${t(currentSort.label)} ${sortArrow}`"
            @click="openMenu"
          >
            <span class="hidden lg:inline">{{ t('drive.sort') }}</span>
            <span>{{ t(currentSort.label) }}</span>
            <span class="text-[10px] text-primary" aria-hidden="true">{{ sortArrow }}</span>
            <ChevronDown16 class="h-3 w-3 text-faint" aria-hidden="true" />
          </summary>
          <div
            class="menu-plate dropdown-content z-50 mt-2 w-[170px] p-1.5"
            role="menu"
            :aria-label="t('drive.sortOrder')"
          >
            <button
              v-for="entry in sorts"
              :key="entry.key"
              type="button"
              role="menuitemradio"
              class="menu-choice"
              :aria-checked="vault.order === entry.key"
              @click="chooseSort(entry.key, $event)"
            >
              <span class="flex-1">{{ t(entry.label) }}</span>
              <span v-if="vault.order === entry.key" class="text-2xs text-faint">
                {{ sortArrow }}
              </span>
              <Checkmark16
                v-if="vault.order === entry.key"
                class="h-3.5 w-3.5 shrink-0 text-primary"
              />
            </button>
          </div>
        </details>

        <details data-toolbar-menu class="dropdown dropdown-end">
          <summary class="menu-trigger" :title="t(currentKind.label)" @click="openMenu">
            <span>{{ t(currentKind.label) }}</span>
            <ChevronDown16 class="h-3 w-3 text-faint" aria-hidden="true" />
          </summary>
          <div
            class="menu-plate dropdown-content z-50 mt-2 w-[160px] p-1.5"
            role="menu"
            :aria-label="t('drive.filterKind')"
          >
            <button
              v-for="entry in kinds"
              :key="entry.key"
              type="button"
              role="menuitemradio"
              class="menu-choice"
              :aria-checked="vault.kind === entry.key"
              @click="chooseKind(entry.key, $event)"
            >
              <span class="flex-1">{{ t(entry.label) }}</span>
              <Checkmark16
                v-if="vault.kind === entry.key"
                class="h-3.5 w-3.5 shrink-0 text-primary"
              />
            </button>
          </div>
        </details>

        <div class="seg" role="group" :aria-label="t('view.label')">
          <button
            v-for="entry in availableViews"
            :key="entry.key"
            type="button"
            class="seg-item size-7 p-0"
            :aria-pressed="vault.viewMode === entry.key"
            :aria-label="t('view.name', { view: viewLabel(entry.key) })"
            :title="t('view.name', { view: viewLabel(entry.key) })"
            @click="vault.storeView(entry.key)"
          >
            <component :is="entry.icon" aria-hidden="true" />
          </button>
        </div>
      </div>
    </div>

    <Transition
      mode="out-in"
      enter-active-class="transition duration-150 ease-out"
      enter-from-class="opacity-0"
      leave-active-class="transition duration-100 ease-in"
      leave-to-class="opacity-0"
    >
      <div
        v-if="hasSelection"
        key="selection"
        class="mt-2 flex min-h-[42px] flex-wrap items-center gap-2 rounded-control bg-primary/12 px-2.5 py-1.5"
      >
        <span class="flex items-center gap-2 pl-1 text-[12px] text-base-content">
          <Checkmark16 class="text-primary" aria-hidden="true" />
          {{ t('status.selectedCount', { n: formatCount(selectionCount) }) }}
          <span class="text-faint">· {{ formatBytes(vault.selectedBytes) }}</span>
        </span>

        <div class="ml-auto flex flex-wrap items-center gap-1">
          <template v-if="mode === 'recycle'">
            <button class="btn btn-outline btn-sm" type="button" @click="restoreSelection">
              <Undo16 />
              {{ t('preview.restore') }}
            </button>
            <button class="btn btn-error btn-sm" type="button" @click="purgeSelection">
              <TrashCan16 />
              {{ t('preview.deleteForever') }}
            </button>
          </template>
          <template v-else>
            <button class="btn btn-outline btn-sm" type="button" @click="moveSelection">
              <Move16 />
              {{ t('preview.move') }}
            </button>
            <button class="btn btn-outline btn-sm" type="button" @click="recycleSelection">
              <TrashCan16 />
              {{ t('preview.recycle') }}
            </button>
          </template>
          <IconButton :label="t('drive.clearSelection')" @click="vault.clearSelection()">
            <Close16 />
          </IconButton>
        </div>
      </div>
    </Transition>
  </div>
</template>
