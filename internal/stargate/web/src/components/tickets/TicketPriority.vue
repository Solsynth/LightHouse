<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import { ticketPriorityLabel } from '@/lib/enums'
import { prioritySignal, priorityText } from '@/lib/tickets'

const props = withDefaults(defineProps<{ priority: number; label?: boolean }>(), {
  label: true,
})

const { t } = useI18n()
const labelText = computed(() => ticketPriorityLabel(props.priority))
const pips = computed(() => Math.min(4, Math.max(1, props.priority + 1)))
</script>

<template>
  <span
    class="inline-flex items-center gap-1.5 text-2xs"
    :class="priorityText(priority)"
    :title="t('support.priorityLabel')"
    :aria-label="`${t('support.priorityLabel')}: ${labelText}`"
  >
    <span class="flex items-end gap-[2px]" aria-hidden="true">
      <span
        v-for="pip in 4"
        :key="pip"
        class="w-[3px] rounded-full transition-colors"
        :class="[
          pip <= pips ? prioritySignal(priority) : 'bg-line-soft',
          pip === 1 ? 'h-1' : pip === 2 ? 'h-1.5' : pip === 3 ? 'h-2' : 'h-2.5',
        ]"
      />
    </span>
    <span v-if="label">{{ labelText }}</span>
  </span>
</template>
