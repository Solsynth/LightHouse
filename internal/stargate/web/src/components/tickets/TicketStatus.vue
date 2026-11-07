<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import { ticketStatusLabel } from '@/lib/enums'
import { statusRail, statusText } from '@/lib/tickets'

const props = withDefaults(defineProps<{ status: number; quiet?: boolean }>(), {
  quiet: false,
})

const { t } = useI18n()
const label = computed(() => ticketStatusLabel(props.status))
</script>

<template>
  <span
    class="inline-flex items-center gap-1.5 text-2xs"
    :class="quiet ? statusText(status) : 'text-dim'"
    :title="t('support.statusLabel')"
  >
    <span class="dot" :class="statusRail(status)" aria-hidden="true" />
    <span>{{ label }}</span>
  </span>
</template>
