<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Plug16 } from '@carbon/icons-vue'

import type { CloudFileRef } from '@/lib/api/types'

/**
 * The asking app's mark on a consent screen. An app registration may carry a
 * Drive picture; when it does not — or when the stored file has gone and the
 * image fails — the mark is a monogram of the app name, and failing that a
 * neutral glyph. A stale reference therefore never leaves a hole in the card.
 *
 * `flat` squares the corner for the app list, where the mark sits next to a
 * ledger row rather than titleing a page.
 */
const props = withDefaults(
  defineProps<{
    picture?: CloudFileRef | null
    name?: string
    /** Rendered box size in pixels. */
    size?: number
    flat?: boolean
  }>(),
  { picture: null, name: '', size: 44, flat: false },
)

const failed = ref(false)

// A replaced picture must get a fresh chance to load, so the failure latch is
// keyed on the file, not on the component's lifetime.
watch(
  () => props.picture?.id,
  () => {
    failed.value = false
  },
)

const url = computed(() => (failed.value ? '' : (props.picture?.url ?? '')))

const monogram = computed(() => {
  const words = (props.name ?? '')
    .trim()
    .split(/[\s\-_.]+/)
    .filter(Boolean)
  if (!words.length) return ''
  if (words.length >= 2) return (words[0]!.slice(0, 1) + words[1]!.slice(0, 1)).toUpperCase()
  return words[0]!.slice(0, 2).toUpperCase()
})

const box = computed(() => ({ width: `${props.size}px`, height: `${props.size}px` }))
</script>

<template>
  <img
    v-if="url"
    :src="url"
    alt=""
    :style="box"
    class="shrink-0 border border-line object-cover"
    :class="flat ? 'rounded-[2px]' : 'rounded-[3px]'"
    @error="failed = true"
  />
  <span
    v-else
    :style="box"
    class="flex shrink-0 items-center justify-center border border-line bg-base-300/40 select-none"
    :class="[flat ? 'rounded-[2px]' : 'rounded-[3px]', monogram ? 'text-base-content' : 'text-faint']"
    aria-hidden="true"
  >
    <span v-if="monogram" class="font-sans font-semibold" :style="{ fontSize: `${Math.round(props.size * 0.36)}px` }">
      {{ monogram }}
    </span>
    <Plug16 v-else />
  </span>
</template>