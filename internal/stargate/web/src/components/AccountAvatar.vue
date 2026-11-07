<script setup lang="ts">
import { UserAvatar16 } from '@carbon/icons-vue'
import { computed, ref } from 'vue'

import type { Account } from '@/lib/api/types'

/**
 * Compact square avatar for an account. The canonical picture endpoint is
 * always tried when the account has a name, because ticket and search payloads
 * hydrate accounts without their profile; a Drive URL is used directly when
 * the profile is present. Missing art and broken images fall back to the
 * generic glyph, and failures are latched against the URL so a reused row
 * never re-requests one that already failed.
 */
const props = defineProps<{ account?: Account }>()

const failedSrc = ref('')

const picture = computed(() => props.account?.profile?.picture)
const src = computed(() => {
  const account = props.account
  if (!account?.name) return ''
  // A profile that resolved without a picture is authoritative; only a
  // profile-less payload needs the canonical endpoint.
  if (account.profile) return picture.value?.url ?? ''
  return `/api/accounts/${encodeURIComponent(account.name)}/picture`
})
const showImage = computed(() => Boolean(src.value) && failedSrc.value !== src.value)
</script>

<template>
  <span
    class="grid size-6 shrink-0 place-items-center overflow-hidden rounded-full bg-base-300"
    :class="showImage ? '' : 'text-faint'"
  >
    <img
      v-if="showImage"
      :src="src"
      alt=""
      class="h-full w-full object-cover"
      loading="lazy"
      decoding="async"
      @error="failedSrc = src"
    />
    <UserAvatar16 v-else aria-hidden="true" />
  </span>
</template>
