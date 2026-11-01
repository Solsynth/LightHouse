<script setup lang="ts">
/**
 * A grouped region of a page: a tonal plate with a caption, a body, and an
 * optional footer. Separation comes from the surface tone and the space
 * around it, never from a rule or border.
 */
withDefaults(
  defineProps<{
    title?: string
    note?: string
    flush?: boolean
  }>(),
  { title: '', note: undefined, flush: false },
)
</script>

<template>
  <section class="overflow-hidden rounded-card bg-base-200/50">
    <header
      v-if="title || note || $slots.header"
      class="flex flex-wrap items-baseline justify-between gap-2 px-6 pb-1.5 pt-5"
    >
      <h2 v-if="title" class="text-2xs uppercase tracking-[0.16em] text-dim">{{ title }}</h2>
      <span v-if="note" class="text-2xs text-faint">{{ note }}</span>
      <slot name="header" />
    </header>
    <div :class="flush ? '' : 'px-6 py-5'">
      <slot />
    </div>
    <footer v-if="$slots.footer" class="bg-base-200/40 px-6 py-3">
      <slot name="footer" />
    </footer>
  </section>
</template>
