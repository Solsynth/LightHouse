/** * The file context menu: a compact plate attached to the object under the * pointer. Its header
carries the object's own glyph and identity, so the * actions read as properties of that file rather
than a generic dropdown. * * It acts on the right-clicked row, or on the whole ticked selection when
the * row is part of it. All four browsing surfaces open this same plate. */
<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Copy16,
  Download16,
  Edit16,
  FolderOpen16,
  Information16,
  Move16,
  TrashCan16,
  Undo16,
} from '@carbon/icons-vue'

import FileGlyph from './FileGlyph.vue'
import { publicFileUrl, openUrl } from '@/lib/api/drive'
import { closeFileMenu, useFileMenu } from '@/lib/fileMenu'
import { displayName, formatBytes, formatCount, kindLabel } from '@/lib/format'
import { useFileOpener } from '@/lib/opening'
import { useNoticeStore } from '@/stores/notice'
import { useUiStore } from '@/stores/ui'
import { useVaultStore } from '@/stores/vault'

const { state: menu } = useFileMenu()
const { t } = useI18n()
const ui = useUiStore()
const vault = useVaultStore()
const notices = useNoticeStore()
const opener = useFileOpener()

const plate = ref<HTMLElement | null>(null)
const position = ref({ left: 0, top: 0 })
let returnFocus: HTMLElement | null = null

const files = computed(() => menu.value?.files ?? [])
const single = computed(() => files.value.length === 1)
const file = computed(() => files.value[0] ?? null)
const allRecycled = computed(
  () => files.value.length > 0 && files.value.every((entry) => entry.is_marked_recycle),
)
const downloadable = computed(() => files.value.filter((entry) => !entry.is_folder))
const publicTarget = computed(
  () =>
    single.value && file.value !== null && file.value.permission_status?.visibility === 'public',
)

const meta = computed(() => {
  if (!file.value) return ''
  if (!single.value) {
    const bytes = files.value.reduce((sum, entry) => sum + (entry.object?.size ?? 0), 0)
    return t('menu.totalSize', { size: formatBytes(bytes) })
  }
  if (file.value.is_folder) {
    return `${t('menu.folder')} · ${formatCount(file.value.children_count ?? 0)}`
  }
  return `${kindLabel(file.value)} · ${formatBytes(file.value.object?.size)}`
})
const heading = computed(() => {
  if (!file.value) return ''
  return single.value
    ? displayName(file.value)
    : t('menu.many', { n: formatCount(files.value.length) })
})

/** Places the plate near the pointer, flipping it before it leaves the viewport. */
async function place() {
  if (!menu.value) return
  await nextTick()
  const rect = plate.value?.getBoundingClientRect()
  const margin = 8
  let left = menu.value.x + 2
  let top = menu.value.y + 2
  if (rect) {
    if (left + rect.width > window.innerWidth - margin) {
      left = Math.max(margin, menu.value.x - rect.width - 2)
    }
    if (top + rect.height > window.innerHeight - margin) {
      top = Math.max(margin, menu.value.y - rect.height - 2)
    }
  }
  position.value = { left, top }
}

watch(
  menu,
  (value) => {
    if (!value) {
      returnFocus?.focus()
      returnFocus = null
      return
    }
    returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
    void place()
    void nextTick(() => {
      const first = plate.value?.querySelector<HTMLElement>('[role="menuitem"]')
      first?.focus()
    })
  },
  { flush: 'post' },
)

function close() {
  closeFileMenu()
}

function run(action: () => void) {
  try {
    action()
  } finally {
    close()
  }
}

function openTarget() {
  if (!file.value) return
  opener.open(file.value)
}

function inspect() {
  if (!file.value) return
  ui.openDialog({ kind: 'inspect', file: file.value })
}

function rename() {
  if (!file.value) return
  ui.openDialog({ kind: 'rename', file: file.value })
}

function move() {
  if (files.value.length === 0) return
  ui.openDialog({ kind: 'move', files: [...files.value] })
}

function permissions() {
  if (!file.value) return
  ui.openDialog({ kind: 'permissions', file: file.value })
}

async function copyLink() {
  if (!file.value) return
  const link = `${window.location.origin}${publicFileUrl(file.value.id)}`
  try {
    await navigator.clipboard.writeText(link)
    notices.ok(t(publicTarget.value ? 'preview.linkCopied' : 'preview.privateLinkCopied'))
  } catch {
    notices.error(t('preview.clipboardDenied'))
  }
}

function download() {
  for (const entry of downloadable.value) {
    const link = document.createElement('a')
    link.href = openUrl(entry.id, true)
    link.download = entry.name || ''
    document.body.append(link)
    link.click()
    link.remove()
  }
}

function recycle() {
  if (files.value.length === 0) return
  ui.openDialog({ kind: 'recycle', files: [...files.value] })
}

function purge() {
  if (files.value.length === 0) return
  ui.openDialog({ kind: 'purge', files: [...files.value] })
}

async function restore() {
  if (files.value.length === 0) return
  await vault.restore([...files.value]).catch(() => undefined)
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault()
    close()
    return
  }
  if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
  const items = [...(plate.value?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [])]
  if (items.length === 0) return
  event.preventDefault()
  const current = items.indexOf(document.activeElement as HTMLElement)
  const delta = event.key === 'ArrowDown' ? 1 : -1
  const next = (current + delta + items.length) % items.length
  items[next]?.focus()
}

function onPointerDown(event: PointerEvent) {
  if (!menu.value) return
  if (plate.value?.contains(event.target as Node)) return
  close()
}

function onViewportChange() {
  if (menu.value) close()
}

onMounted(() => {
  window.addEventListener('pointerdown', onPointerDown, true)
  window.addEventListener('keydown', onKeydown, true)
  window.addEventListener('resize', onViewportChange)
  window.addEventListener('blur', onViewportChange)
  document.addEventListener('scroll', onViewportChange, true)
})

onBeforeUnmount(() => {
  window.removeEventListener('pointerdown', onPointerDown, true)
  window.removeEventListener('keydown', onKeydown, true)
  window.removeEventListener('resize', onViewportChange)
  window.removeEventListener('blur', onViewportChange)
  document.removeEventListener('scroll', onViewportChange, true)
})
</script>

<template>
  <Teleport to="body">
    <Transition
      enter-active-class="transition duration-150 ease-out"
      enter-from-class="opacity-0 scale-[0.96]"
      leave-active-class="transition duration-100 ease-in"
      leave-to-class="opacity-0"
    >
      <div
        v-if="menu && file"
        ref="plate"
        class="fixed z-[80]"
        :style="{ left: `${position.left}px`, top: `${position.top}px` }"
        role="menu"
        :aria-label="t('menu.actions')"
        @click.stop
      >
        <div :key="menu.key" class="menu-plate w-[264px] p-1.5" style="transform-origin: top left">
          <header class="flex items-center gap-2.5 rounded-chip px-2 pb-2 pt-1.5">
            <span class="grid size-9 shrink-0 place-items-center rounded-chip bg-base-300/45">
              <FileGlyph :file="file" size="md" />
            </span>
            <div class="min-w-0 flex-1">
              <p class="truncate text-[12.5px] font-semibold text-base-content" :title="heading">
                {{ heading }}
              </p>
              <p class="mt-0.5 truncate text-[11px] tabular-nums text-faint">{{ meta }}</p>
            </div>
          </header>

          <div class="flex flex-col gap-0.5">
            <button
              v-if="single"
              type="button"
              role="menuitem"
              class="menu-choice"
              @click="run(openTarget)"
            >
              <FolderOpen16 class="h-4 w-4 shrink-0 text-faint" aria-hidden="true" />
              <span class="flex-1 truncate">
                {{ file.is_folder ? t('menu.openFolder') : t('menu.open') }}
              </span>
              <span class="kbd text-[10px]">↵</span>
            </button>
            <button
              v-if="single"
              type="button"
              role="menuitem"
              class="menu-choice"
              @click="run(inspect)"
            >
              <Information16 class="h-4 w-4 shrink-0 text-faint" aria-hidden="true" />
              <span class="flex-1 truncate">{{ t('menu.inspect') }}</span>
            </button>
          </div>

          <div class="flex flex-col gap-0.5 pt-0.5">
            <button
              v-if="single"
              type="button"
              role="menuitem"
              class="menu-choice"
              @click="run(rename)"
            >
              <Edit16 class="h-4 w-4 shrink-0 text-faint" aria-hidden="true" />
              <span class="flex-1 truncate">{{ t('preview.rename') }}</span>
            </button>
            <button type="button" role="menuitem" class="menu-choice" @click="run(move)">
              <Move16 class="h-4 w-4 shrink-0 text-faint" aria-hidden="true" />
              <span class="flex-1 truncate">{{ t('preview.move') }}</span>
            </button>
            <button
              v-if="single"
              type="button"
              role="menuitem"
              class="menu-choice"
              @click="run(permissions)"
            >
              <svg
                class="h-4 w-4 shrink-0 text-faint"
                viewBox="0 0 16 16"
                fill="none"
                stroke="currentColor"
                stroke-width="1.25"
                stroke-linecap="round"
                aria-hidden="true"
              >
                <path d="M5 7V4.5a3 3 0 0 1 6 0V7" />
                <rect x="3.25" y="7" width="9.5" height="6.5" rx="1.75" />
              </svg>
              <span class="flex-1 truncate">{{ t('menu.permissions') }}</span>
            </button>
            <button
              v-if="single"
              type="button"
              role="menuitem"
              class="menu-choice"
              @click="run(copyLink)"
            >
              <Copy16 class="h-4 w-4 shrink-0 text-faint" aria-hidden="true" />
              <span class="flex-1 truncate">{{ t('preview.copyLink') }}</span>
            </button>
            <button
              v-if="downloadable.length > 0"
              type="button"
              role="menuitem"
              class="menu-choice"
              @click="run(download)"
            >
              <Download16 class="h-4 w-4 shrink-0 text-faint" aria-hidden="true" />
              <span class="flex-1 truncate">{{ t('preview.download') }}</span>
            </button>
          </div>

          <div class="flex flex-col gap-0.5 pt-0.5">
            <button
              v-if="allRecycled"
              type="button"
              role="menuitem"
              class="menu-choice"
              @click="run(restore)"
            >
              <Undo16 class="h-4 w-4 shrink-0 text-faint" aria-hidden="true" />
              <span class="flex-1 truncate">{{ t('preview.restore') }}</span>
            </button>
            <button
              v-else
              type="button"
              role="menuitem"
              class="menu-choice menu-choice-danger"
              @click="run(recycle)"
            >
              <TrashCan16 class="h-4 w-4 shrink-0" aria-hidden="true" />
              <span class="flex-1 truncate">{{ t('preview.recycle') }}</span>
            </button>
            <button
              v-if="allRecycled"
              type="button"
              role="menuitem"
              class="menu-choice menu-choice-danger"
              @click="run(purge)"
            >
              <TrashCan16 class="h-4 w-4 shrink-0" aria-hidden="true" />
              <span class="flex-1 truncate">{{ t('preview.deleteForever') }}</span>
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>
