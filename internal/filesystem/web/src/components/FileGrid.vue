<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { CopyLink16 } from '@carbon/icons-vue'

import FileGlyph from './FileGlyph.vue'
import type { CloudFile } from '@/lib/api/types'
import { previewUrl } from '@/lib/api/drive'
import { displayName, formatBytes, formatCount, kindLabel } from '@/lib/format'
import { openFileMenu } from '@/lib/fileMenu'
import { hasPreview } from '@/lib/fileKinds'
import { useFileOpener } from '@/lib/opening'
import { useVaultStore } from '@/stores/vault'

const vault = useVaultStore()
const opener = useFileOpener()
const { t } = useI18n()

const failed = ref<string[]>([])

function open(file: CloudFile) {
  if (vault.selectMode) {
    vault.toggle(file)
    return
  }
  opener.open(file)
}

function onKeydown(event: KeyboardEvent, file: CloudFile) {
  if (event.key === 'Enter') {
    event.preventDefault()
    open(file)
    return
  }
  if (event.key === ' ' && vault.selectMode) {
    event.preventDefault()
    vault.toggle(file)
    return
  }
  if (event.key === 'ArrowRight' || event.key === 'ArrowLeft') {
    event.preventDefault()
    const card = event.currentTarget as HTMLElement
    const sibling =
      event.key === 'ArrowRight' ? card.nextElementSibling : card.previousElementSibling
    if (sibling instanceof HTMLElement) sibling.focus()
  }
}

function showPreview(file: CloudFile): boolean {
  return hasPreview(file) && !failed.value.includes(file.id)
}

function markFailed(id: string) {
  if (!failed.value.includes(id)) failed.value = [...failed.value, id]
}

function isPublic(file: CloudFile): boolean {
  return file.permission_status?.visibility === 'public'
}

function subline(file: CloudFile): string {
  if (file.is_folder) {
    const count = file.children_count ?? 0
    return count === 1 ? t('drive.oneItem') : t('drive.manyItems', { n: formatCount(count) })
  }
  return `${kindLabel(file)} · ${formatBytes(file.object?.size)}`
}
</script>

<template>
  <div class="min-h-0 flex-1 overflow-auto scroll-region p-2 md:p-3">
    <div
      class="grid grid-cols-2 gap-2.5 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5 2xl:grid-cols-6"
      role="list"
      :aria-label="t('drive.objects')"
    >
      <article
        v-for="(file, index) in vault.items"
        :key="file.id"
        tabindex="0"
        role="listitem"
        class="row-in group relative flex cursor-pointer flex-col rounded-card p-2 transition-colors duration-200"
        :class="
          vault.selection.includes(file.id)
            ? 'bg-primary/12'
            : 'bg-base-200/75 hover:bg-base-300/70'
        "
        :style="{ animationDelay: `${Math.min(index, 12) * 18}ms` }"
        :aria-selected="vault.selection.includes(file.id)"
        @click="open(file)"
        @contextmenu="openFileMenu($event, file, vault.selectedItems)"
        @keydown="onKeydown($event, file)"
      >
        <div class="relative aspect-[4/3] overflow-hidden rounded-chip bg-base-300/40">
          <img
            v-if="showPreview(file)"
            :src="previewUrl(file.id)"
            :alt="''"
            loading="lazy"
            decoding="async"
            class="h-full w-full object-cover opacity-90 transition-opacity duration-200 group-hover:opacity-100"
            @error="markFailed(file.id)"
          />
          <div v-else class="grid h-full place-items-center">
            <FileGlyph :file="file" size="lg" class="opacity-70" />
          </div>

          <input
            v-if="vault.selectMode"
            type="checkbox"
            class="checkbox checkbox-xs absolute left-2 top-2 bg-base-100/85"
            :checked="vault.selection.includes(file.id)"
            :aria-label="t('drive.selectObject', { name: displayName(file) })"
            @change="vault.toggle(file, ($event as MouseEvent).shiftKey)"
            @click.stop
          />

          <CopyLink16
            v-if="isPublic(file)"
            class="absolute right-2 top-2 h-3.5 w-3.5 text-primary/70"
            :title="t('drive.publishedByLink')"
          />
        </div>

        <div class="flex min-w-0 flex-col gap-0.5 px-1 pb-0.5 pt-2.5">
          <span
            class="truncate text-[12.5px] font-semibold text-base-content"
            :title="displayName(file)"
          >
            {{ displayName(file) }}
          </span>
          <span class="truncate text-[11px] text-faint">
            {{ subline(file) }}
          </span>
        </div>
      </article>
    </div>
  </div>
</template>
