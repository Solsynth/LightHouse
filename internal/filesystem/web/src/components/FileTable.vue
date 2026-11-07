<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Link16 } from '@carbon/icons-vue'

import FileGlyph from './FileGlyph.vue'
import type { CloudFile } from '@/lib/api/types'
import { displayName, formatBytes, kindLabel, modifiedLabel } from '@/lib/format'
import { useFileOpener } from '@/lib/opening'
import { useVaultStore } from '@/stores/vault'

const vault = useVaultStore()
const opener = useFileOpener()
const { t } = useI18n()

const allSelected = computed(
  () => vault.items.length > 0 && vault.selection.length === vault.items.length,
)
const someSelected = computed(
  () => vault.selection.length > 0 && vault.selection.length < vault.items.length,
)

function isPublic(file: CloudFile): boolean {
  return file.permission_status?.visibility === 'public'
}

function open(file: CloudFile) {
  opener.open(file)
}

function onKeydown(event: KeyboardEvent, file: CloudFile) {
  if (event.key === 'Enter') {
    event.preventDefault()
    open(file)
    return
  }
  if (event.key === ' ') {
    event.preventDefault()
    vault.toggle(file.id)
    return
  }
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    const row = event.currentTarget as HTMLElement
    const sibling = event.key === 'ArrowDown' ? row.nextElementSibling : row.previousElementSibling
    if (sibling instanceof HTMLElement) sibling.focus()
  }
}

function toggleAll() {
  if (allSelected.value) vault.clearSelection()
  else vault.selectAll()
}

function sortBy(key: 'name' | 'size' | 'date') {
  vault.setOrder(key)
}

function indicator(key: 'name' | 'size' | 'date') {
  if (vault.order !== key) return ''
  return vault.orderDesc ? '↓' : '↑'
}

function sortState(key: 'name' | 'size' | 'date') {
  if (vault.order !== key) return 'none'
  return vault.orderDesc ? 'descending' : 'ascending'
}
</script>

<template>
  <div class="min-h-0 flex-1 overflow-auto scroll-region px-2 pb-2">
    <table class="ledger w-full table-fixed">
      <thead class="sticky top-0 z-10 bg-base-200">
        <tr class="text-[11px] font-medium text-faint">
          <th scope="col" class="w-10 pl-3 pr-1 text-left">
            <span class="sr-only">{{ t('drive.selectColumn') }}</span>
            <input
              type="checkbox"
              class="checkbox checkbox-xs"
              :checked="allSelected"
              :indeterminate.prop="someSelected"
              :disabled="vault.items.length === 0"
              :aria-label="t('drive.selectAllObjects')"
              @change="toggleAll"
            />
          </th>
          <th scope="col" class="min-w-0 text-left" :aria-sort="sortState('name')">
            <button
              type="button"
              class="flex h-9 w-full items-center gap-1 font-bold transition-colors hover:text-dim"
              :class="vault.order === 'name' ? 'text-base-content' : ''"
              @click="sortBy('name')"
            >
              {{ t('drive.sortName') }}
              <span v-if="indicator('name')" class="text-primary">{{ indicator('name') }}</span>
            </button>
          </th>
          <th scope="col" class="hidden w-[100px] text-left md:table-cell">{{ t('meta.kind') }}</th>
          <th
            scope="col"
            class="hidden w-[92px] pr-3 text-right sm:table-cell"
            :aria-sort="sortState('size')"
          >
            <button
              type="button"
              class="flex h-9 w-full items-center justify-end gap-1 font-bold transition-colors hover:text-dim"
              :class="vault.order === 'size' ? 'text-base-content' : ''"
              @click="sortBy('size')"
            >
              {{ t('drive.sortSize') }}
              <span v-if="indicator('size')" class="text-primary">{{ indicator('size') }}</span>
            </button>
          </th>
          <th
            scope="col"
            class="hidden w-[132px] pr-3 text-right lg:table-cell"
            :aria-sort="sortState('date')"
          >
            <button
              type="button"
              class="flex h-9 w-full items-center justify-end gap-1 font-bold transition-colors hover:text-dim"
              :class="vault.order === 'date' ? 'text-base-content' : ''"
              @click="sortBy('date')"
            >
              {{ t('meta.modified') }}
              <span v-if="indicator('date')" class="text-primary">{{ indicator('date') }}</span>
            </button>
          </th>
        </tr>
      </thead>

      <tbody v-if="vault.loading && vault.items.length === 0">
        <tr v-for="row in 7" :key="`skeleton-${row}`">
          <td class="pl-3 pr-1"><span class="block h-3 w-3 rounded-[6px] bg-base-300" /></td>
          <td>
            <div class="flex items-center gap-2.5">
              <span class="size-4 rounded-[6px] bg-base-300 pulse-soft" />
              <span class="h-3 w-40 rounded-[6px] bg-base-300 pulse-soft" />
            </div>
          </td>
          <td class="hidden md:table-cell">
            <span class="block h-3 w-12 rounded-[6px] bg-base-300/70" />
          </td>
          <td class="hidden sm:table-cell">
            <span class="ml-auto block h-3 w-10 rounded-[6px] bg-base-300/70" />
          </td>
          <td class="hidden lg:table-cell">
            <span class="ml-auto block h-3 w-14 rounded-[6px] bg-base-300/70" />
          </td>
        </tr>
      </tbody>

      <tbody v-else>
        <tr
          v-for="(file, index) in vault.items"
          :key="file.id"
          tabindex="0"
          class="row-in group h-[46px] cursor-default"
          :class="vault.selection.includes(file.id) ? 'is-selected' : ''"
          :style="{ animationDelay: `${Math.min(index, 12) * 16}ms` }"
          :aria-selected="vault.selection.includes(file.id)"
          @click="open(file)"
          @keydown="onKeydown($event, file)"
        >
          <td class="pl-3 pr-1">
            <input
              type="checkbox"
              class="checkbox checkbox-xs transition-opacity duration-150"
              :class="
                vault.selection.includes(file.id)
                  ? 'opacity-100'
                  : vault.selectionMode
                    ? 'opacity-100'
                    : 'opacity-0 focus-visible:opacity-100 group-hover:opacity-100'
              "
              :checked="vault.selection.includes(file.id)"
              :aria-label="t('drive.selectObject', { name: displayName(file) })"
              @change="vault.toggle(file.id, ($event as MouseEvent).shiftKey)"
              @click.stop
            />
          </td>
          <td class="min-w-0 py-1.5">
            <div class="flex min-w-0 items-center gap-2.5">
              <FileGlyph :file="file" />
              <div class="flex min-w-0 items-center gap-1.5">
                <span class="truncate text-[13px] font-medium" :title="displayName(file)">
                  {{ displayName(file) }}
                </span>
                <Link16
                  v-if="isPublic(file)"
                  class="h-3 w-3 shrink-0 text-primary/70"
                  :title="t('drive.publishedByLink')"
                />
              </div>
            </div>
          </td>
          <td class="hidden text-[12px] text-faint md:table-cell">{{ kindLabel(file) }}</td>
          <td class="hidden pr-3 text-right text-[12px] tabular-nums text-dim sm:table-cell">
            {{ file.is_folder ? '—' : formatBytes(file.object?.size) }}
          </td>
          <td
            class="hidden whitespace-nowrap pr-3 text-right text-[12px] tabular-nums text-faint lg:table-cell"
          >
            {{ modifiedLabel(file.updated_at) }}
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>
