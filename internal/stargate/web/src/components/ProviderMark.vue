<script setup lang="ts">
import { computed } from 'vue'

import { providerInfo } from '@/lib/providers'

/**
 * A provider's brand mark, drawn in the current text color so it repaints with
 * the theme and stays legible in both. A slug with no silhouette in the catalog
 * (an unknown or newly added provider) gets its initial in the same box, so a
 * button is never rendered without a leading mark.
 */
interface Props {
  provider: string
}

const props = defineProps<Props>()

const paths = computed(() => providerInfo(props.provider)?.paths ?? [])
const monogram = computed(() => props.provider.charAt(0).toUpperCase())
</script>

<template>
  <svg
    v-if="paths.length"
    class="h-4 w-4 shrink-0"
    viewBox="0 0 24 24"
    fill="currentColor"
    aria-hidden="true"
  >
    <path v-for="(path, index) in paths" :key="index" :d="path" />
  </svg>
  <span
    v-else
    class="flex h-4 w-4 shrink-0 items-center justify-center text-2xs leading-none"
    aria-hidden="true"
    >{{ monogram }}</span
  >
</template>
