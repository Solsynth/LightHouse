<script setup lang="ts">
import { computed } from 'vue'
import { SkeletonBox } from 'skullet/vue'

/**
 * Loading placeholder: the shape of the content that is about to render.
 *
 * A surface that has not resolved yet shows the same row height, column count
 * and spacing as its loaded state, so a reader sees that something is
 * arriving instead of a page that looks broken, and the real content lands
 * where the placeholder was. Skullet draws every bar: the shimmer phase is
 * shared across the page, the fill and highlight follow the console theme, and
 * reduce-motion holds them still. Bars carry no text, so assistive technology
 * never reads a placeholder row as content.
 *
 * `variant="columns"` (the default) is a table: one bar per column, split by
 * the supplied width ratios. `variant="rows"` is a list: two tiers per row
 * (title plus chips, then a description line), matching the factor, contact
 * and connection lists.
 */
const props = withDefaults(
  defineProps<{
    /** Row count for table and list placeholders. */
    rows?: number
    /** Flex width ratios per column, e.g. `[3, 2, 1]`. */
    columns?: number[]
    /** Bar height for body rows, as a CSS length. */
    height?: string
    /** Draw hairline row separators, matching `table.ledger`. */
    rules?: boolean
    /** Show a header row of shorter bars above the body rows. */
    header?: boolean
    /** Shape to draw: table columns or two-tier list rows. */
    variant?: 'columns' | 'rows'
    /** Center the placeholder inside a min-height box. */
    compact?: boolean
  }>(),
  {
    rows: 4,
    columns: () => [3, 2],
    height: '0.75rem',
    rules: true,
    header: false,
    variant: 'columns',
    compact: false,
  },
)

const total = computed(() => props.columns.reduce((sum, width) => sum + width, 0))
const widthOf = (width: number) => `${(width / total.value) * 100}%`
</script>

<template>
  <div :class="compact ? 'flex min-h-32 items-center' : ''">
    <div class="w-full">
      <div v-if="variant === 'rows'" class="flex flex-col gap-1.5">
        <div
          v-for="row in rows"
          :key="`row-${row}`"
          class="space-y-2 px-3 py-3"
          :class="rules ? 'rounded-chip bg-raised' : ''"
        >
          <div class="flex items-center gap-2">
            <SkeletonBox height="0.75rem" width="8rem" aria-hidden="true" />
            <SkeletonBox height="1rem" width="3.5rem" aria-hidden="true" />
          </div>
          <SkeletonBox height="0.5rem" width="100%" class="max-w-md" aria-hidden="true" />
          <SkeletonBox height="0.5rem" width="6rem" aria-hidden="true" />
        </div>
      </div>

      <template v-else>
        <div v-if="header" class="flex gap-4 pb-2 px-3">
          <SkeletonBox
            v-for="(width, index) in columns"
            :key="`head-${index}`"
            height="0.5rem"
            :width="widthOf(width)"
            aria-hidden="true"
          />
        </div>
        <div class="flex flex-col gap-1.5">
          <div
            v-for="row in rows"
            :key="row"
            class="flex items-center gap-4 px-3 py-3"
            :class="rules ? 'rounded-chip bg-raised' : ''"
          >
            <SkeletonBox
              v-for="(width, index) in columns"
              :key="`${row}-${index}`"
              :height="height"
              :width="widthOf(width)"
              aria-hidden="true"
            />
          </div>
        </div>
      </template>
    </div>
  </div>
</template>
