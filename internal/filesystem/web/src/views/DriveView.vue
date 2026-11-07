<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import {
  ArrowDown16,
  FolderAdd16,
  FolderOpen16,
  Search16,
  TrashCan16,
  Upload16,
  WarningAlt16,
} from '@carbon/icons-vue'

import FileColumnView from '@/components/FileColumnView.vue'
import FileGrid from '@/components/FileGrid.vue'
import FileTable from '@/components/FileTable.vue'
import FileToolbar from '@/components/FileToolbar.vue'
import FileWaterfall from '@/components/FileWaterfall.vue'
import type { BrowsingMode } from '@/stores/vault'
import { useUiStore } from '@/stores/ui'
import { useUploadStore } from '@/stores/upload'
import { useVaultStore } from '@/stores/vault'

const vault = useVaultStore()
const uploads = useUploadStore()
const ui = useUiStore()
const route = useRoute()
const { t } = useI18n()

const dragging = ref(false)
let dragDepth = 0

// Preview and storage are separate routes with their own views, so this view
// only ever browses the three listing modes.
const mode = computed<BrowsingMode>(() => {
  const current = route.meta.mode
  return current === 'unindexed' || current === 'recycle' ? current : 'drive'
})
const folderParam = computed(() => (typeof route.params.id === 'string' ? route.params.id : null))
const onDrive = computed(() => mode.value === 'drive')
const viewKey = computed(() => `${route.fullPath}:${vault.viewMode}`)
// Root and mode crumbs carry a symbolic name, so the label is resolved here
// (inside a computed) rather than stored: `t()` subscribes this to the locale,
// which keeps the heading and breadcrumb correct after a language switch.
const pageTitle = computed(() => {
  if (mode.value === 'recycle') return t('nav.recycle')
  if (mode.value === 'unindexed') return t('nav.unindexed')
  const last = vault.crumbs[vault.crumbs.length - 1]
  if (last && last.id !== null) return last.name
  return t('nav.drive')
})

const emptyState = computed(() => {
  if (vault.query.trim()) {
    return {
      title: t('empty.noMatches'),
      body: t('empty.noMatchesBody', { query: vault.query.trim() }),
      icon: Search16,
    }
  }
  if (vault.poolFilter) {
    return {
      title: t('empty.noPoolFiles'),
      body: t('empty.noPoolFilesBody'),
      icon: FolderOpen16,
    }
  }
  if (mode.value === 'recycle') {
    return {
      title: t('empty.recycleEmpty'),
      body: t('empty.recycleBody'),
      icon: TrashCan16,
    }
  }
  if (mode.value === 'unindexed') {
    return {
      title: t('empty.indexed'),
      body: t('empty.indexedBody'),
      icon: WarningAlt16,
    }
  }
  return {
    title: t('empty.folder'),
    body: t('empty.folderBody'),
    icon: FolderOpen16,
  }
})

watch(
  [mode, folderParam],
  ([nextMode, nextFolder]) => {
    if (nextMode === 'unindexed' && vault.viewMode === 'column') vault.storeView('list')
    void vault.open(nextMode, nextFolder)
  },
  { immediate: true },
)


function requestUpload() {
  window.dispatchEvent(new Event('snfs:upload'))
}

function onDragEnter(event: DragEvent) {
  if (!onDrive.value || !event.dataTransfer?.types.includes('Files')) return
  event.preventDefault()
  dragDepth += 1
  dragging.value = true
}

function onDragOver(event: DragEvent) {
  if (!onDrive.value || !event.dataTransfer?.types.includes('Files')) return
  event.preventDefault()
  event.dataTransfer.dropEffect = 'copy'
}

function onDragLeave() {
  dragDepth = Math.max(0, dragDepth - 1)
  if (dragDepth === 0) dragging.value = false
}

function onDrop(event: DragEvent) {
  dragDepth = 0
  dragging.value = false
  if (!onDrive.value) return
  const list = Array.from(event.dataTransfer?.files ?? [])
  if (list.length === 0) return
  uploads.enqueue(list, vault.folderId)
}

function clearPool() {
  vault.setPoolFilter(null)
}

function retry() {
  void vault.load()
}
</script>

<template>
  <div
    class="relative flex min-h-0 flex-1 flex-col"
    @dragenter="onDragEnter"
    @dragover="onDragOver"
    @dragleave="onDragLeave"
    @drop.prevent="onDrop"
  >
    <FileToolbar />

    <h1 class="sr-only">{{ pageTitle }}</h1>

    <div class="relative flex min-h-0 flex-1 flex-col">
      <div
        v-if="vault.loading && vault.items.length === 0 && vault.viewMode === 'grid'"
        class="grid flex-1 auto-rows-min grid-cols-2 gap-2.5 overflow-hidden p-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5 2xl:grid-cols-6 md:p-5"
      >
        <div v-for="row in 8" :key="row" class="rounded-card bg-base-300/40 p-2">
          <div class="aspect-[4/3] rounded-chip bg-base-100/40 pulse-soft" />
          <div class="space-y-2 px-1 pb-1 pt-2.5">
            <div class="h-2.5 w-3/4 rounded-[6px] bg-base-100/55" />
            <div class="h-2 w-1/2 rounded-[6px] bg-base-100/35" />
          </div>
        </div>
      </div>

      <FileColumnView
        v-else-if="
          mode !== 'unindexed' &&
          vault.viewMode === 'column' &&
          (vault.items.length > 0 || vault.loading)
        "
        :key="viewKey"
      />
      <FileWaterfall
        v-else-if="vault.viewMode === 'waterfall' && (vault.items.length > 0 || vault.loading)"
        :key="viewKey"
      />
      <FileGrid v-else-if="vault.viewMode === 'grid' && vault.items.length > 0" :key="viewKey" />
      <FileTable
        v-else-if="vault.viewMode === 'list' && (vault.items.length > 0 || vault.loading)"
        :key="viewKey"
      />

      <div
        v-if="vault.error && !vault.loading"
        class="absolute inset-0 grid place-items-center overflow-y-auto bg-base-200 px-5"
      >
        <div class="w-full max-w-[380px] rounded-sheet bg-base-300/50 px-6 py-8 text-center">
          <WarningAlt16 class="mx-auto h-6 w-6 text-error" />
          <h2 class="title mt-3 text-sm text-base-content">{{ t('error.loadFolder') }}</h2>
          <p class="voice mt-1.5">{{ vault.error }}</p>
          <button class="btn btn-primary btn-sm mt-5" type="button" @click="retry">
            {{ t('error.tryAgain') }}
          </button>
        </div>
      </div>

      <div
        v-else-if="vault.isEmpty && !vault.loading"
        class="rise flex flex-1 flex-col items-center justify-center overflow-y-auto px-6 py-10 text-center"
      >
        <span class="grid size-14 place-items-center rounded-card bg-base-300/40">
          <component :is="emptyState.icon" class="h-6 w-6 text-faint" aria-hidden="true" />
        </span>
        <h2 class="title mt-4 text-sm text-base-content">{{ emptyState.title }}</h2>
        <p class="voice mt-1.5 max-w-[380px] text-balance">{{ emptyState.body }}</p>

        <div class="mt-5 flex flex-wrap items-center justify-center gap-2">
          <template v-if="vault.query.trim()">
            <button class="btn btn-outline btn-sm" type="button" @click="vault.setQuery('')">
              {{ t('drive.clearSearch') }}
            </button>
          </template>
          <template v-else-if="vault.poolFilter">
            <button class="btn btn-outline btn-sm" type="button" @click="clearPool">
              {{ t('drive.clearPoolFilter') }}
            </button>
          </template>
          <template v-else-if="onDrive">
            <button class="btn btn-outline btn-sm" type="button" @click="requestUpload">
              <Upload16 />
              {{ t('app.uploadFiles') }}
            </button>
            <button
              class="btn btn-outline btn-sm"
              type="button"
              @click="ui.openDialog({ kind: 'new-folder' })"
            >
              <FolderAdd16 />
              {{ t('drive.newFolder') }}
            </button>
          </template>
        </div>
      </div>
    </div>

    <footer
      v-if="vault.items.length > 0 && vault.hasMore && vault.viewMode !== 'waterfall'"
      class="flex h-12 shrink-0 items-center justify-center px-4"
    >
      <button
        class="btn btn-ghost btn-sm"
        type="button"
        :disabled="vault.loadingMore"
        @click="vault.loadMore()"
      >
        {{ vault.loadingMore ? t('drive.loadingMore') : t('drive.loadMore') }}
        <ArrowDown16 />
      </button>
    </footer>

    <Transition
      enter-active-class="transition duration-150 ease-out"
      enter-from-class="opacity-0"
      leave-active-class="transition duration-100 ease-in"
      leave-to-class="opacity-0"
    >
      <div
        v-if="dragging"
        class="pointer-events-none absolute inset-0 z-20 grid place-items-center bg-base-100/60 p-6 backdrop-blur-sm"
      >
        <div class="rounded-sheet bg-tint px-10 py-9 text-center">
          <span class="mx-auto grid size-12 place-items-center rounded-card bg-primary/15">
            <Upload16 class="h-5 w-5 text-primary" />
          </span>
          <div class="title mt-3 text-sm text-base-content">{{ t('drop.title') }}</div>
          <div class="voice mt-1 text-2xs">
            {{ t('drop.body', { location: vault.folder ? vault.folder.name : t('nav.drive') }) }}
          </div>
        </div>
      </div>
    </Transition>
  </div>
</template>
