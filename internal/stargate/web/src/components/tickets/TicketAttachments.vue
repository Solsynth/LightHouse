<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Close16, Document16 } from '@carbon/icons-vue'

import type { CloudFileRef } from '@/lib/api/types'
import type { PickerFile } from '@/lib/picker-types'
import { bytes } from '@/lib/format'
import { resolveFileUrl } from '@/lib/media'

type AttachmentFile = CloudFileRef | PickerFile

const props = withDefaults(
  defineProps<{
    files: AttachmentFile[]
    removable?: boolean
  }>(),
  { removable: false },
)

const emit = defineEmits<{ remove: [id: string] }>()

const { t } = useI18n()

const failed = ref<string[]>([])

function isImage(file: AttachmentFile): boolean {
  return (file.mime_type ?? '').startsWith('image/')
}

function href(file: AttachmentFile): string {
  return resolveFileUrl(file)
}

function thumbnail(file: AttachmentFile): string {
  if ('thumbnail_url' in file && file.thumbnail_url) return file.thumbnail_url
  return resolveFileUrl(file, 'thumbnail')
}

function name(file: AttachmentFile): string {
  return file.name || file.id
}

function markFailed(id: string) {
  if (!failed.value.includes(id)) failed.value.push(id)
}
</script>

<template>
  <ul v-if="props.files.length" class="grid gap-2 sm:grid-cols-2">
    <li
      v-for="file in props.files"
      :key="file.id"
      class="flex min-w-0 items-center gap-2.5 rounded-chip bg-base-300/45 p-2"
    >
      <a
        :href="href(file)"
        target="_blank"
        rel="noopener noreferrer"
        class="shrink-0"
        :aria-label="name(file)"
      >
        <img
          v-if="isImage(file) && !failed.includes(file.id)"
          :src="thumbnail(file)"
          alt=""
          class="size-10 rounded-mark bg-base-100 object-cover"
          loading="lazy"
          decoding="async"
          @error="markFailed(file.id)"
        />
        <span v-else class="grid size-10 place-items-center rounded-mark bg-base-100 text-faint">
          <Document16 aria-hidden="true" />
        </span>
      </a>

      <div class="min-w-0 flex-1">
        <a
          :href="href(file)"
          target="_blank"
          rel="noopener noreferrer"
          class="block truncate text-2xs text-dim hover:text-base-content hover:underline"
        >
          {{ name(file) }}
        </a>
        <p v-if="file.size" class="mt-0.5 text-2xs text-faint">{{ bytes(file.size) }}</p>
      </div>

      <button
        v-if="props.removable"
        type="button"
        class="grid size-7 shrink-0 place-items-center rounded-chip text-faint transition-colors hover:bg-base-300 hover:text-base-content"
        :aria-label="t('support.detail.removeAttachment', { name: name(file) })"
        @click="emit('remove', file.id)"
      >
        <Close16 aria-hidden="true" />
      </button>
    </li>
  </ul>
</template>
