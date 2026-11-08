<script setup lang="ts">
/**
 * Compact icon-only action for table rows and list items. The label is both
 * the accessible name and the hover tooltip, so the icon never stands alone
 * without an explanation.
 */
const props = defineProps<{
  label: string
  tone?: 'neutral' | 'danger'
  disabled?: boolean
  /** Draws the control in its on state, for toggles like selection mode. */
  active?: boolean
}>()

const emit = defineEmits<{ click: [] }>()
</script>

<template>
  <button
    type="button"
    class="btn btn-ghost btn-xs px-1"
    :class="
      props.active
        ? 'bg-primary/12 text-primary hover:text-primary'
        : props.tone === 'danger'
          ? 'text-dim hover:text-error'
          : 'text-dim hover:text-base-content'
    "
    :title="props.label"
    :aria-label="props.label"
    :aria-pressed="props.active === undefined ? undefined : props.active"
    :disabled="props.disabled"
    @click="emit('click')"
  >
    <slot />
  </button>
</template>
