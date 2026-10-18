<script setup lang="ts">
import { UserAvatar16 } from '@carbon/icons-vue'
import { computed, ref } from 'vue'

import type { Account } from '@/lib/api/types'

/**
 * Compact square avatar for an account. The picture is only attempted when the
 * server resolved a profile reference; the canonical endpoint is the source for
 * a reference that carries only a Drive id. A missing account, a missing
 * picture, and a broken image all fall back to the generic glyph. Failures are
 * recorded against the resolved URL, so a reused row still tries a different
 * account's art but never re-requests one that already failed.
 */
const props = defineProps<{ account?: Account }>()

const failedSrc = ref('')

const picture = computed(() => props.account?.profile?.picture)
const src = computed(() =>
  picture.value
    ? picture.value.url ||
      (props.account?.name ? `/api/accounts/${encodeURIComponent(props.account.name)}/picture` : '')
    : '',
)
const showImage = computed(() => Boolean(src.value) && failedSrc.value !== src.value)
</script>

<template>
  <span
    class="grid size-6 shrink-0 place-items-center overflow-hidden rounded-[2px] bg-base-300"
    :class="showImage ? '' : 'text-faint'"
  >
    <img
      v-if="showImage"
      :src="src"
      alt=""
      class="h-full w-full object-cover"
      @error="failedSrc = src"
    />
    <UserAvatar16 v-else aria-hidden="true" />
  </span>
</template>
