<script setup lang="ts">
/**
 * A grouped region of a page: a tonal plate with a caption, a body, and an
 * optional footer. Separation comes from the surface tone and the space
 * around it, never from a rule inside the body; a table that reaches the
 * panel edge closes itself with its own header rule.
 */
defineProps<{
  title: string
  note?: string
}>()
</script>

<template>
  <section class="overflow-hidden rounded-card border border-line bg-base-200/30">
    <header class="flex flex-wrap items-baseline justify-between gap-2 px-6 pb-1.5 pt-5">
      <h2 class="text-2xs uppercase tracking-[0.16em] text-dim">{{ title }}</h2>
      <span v-if="note" class="text-2xs text-faint">{{ note }}</span>
      <slot name="header" />
    </header>
    <div class="px-6 py-5">
      <slot />
    </div>
    <footer v-if="$slots.footer" class="px-6 pb-4 pt-1">
      <slot name="footer" />
    </footer>
  </section>
</template>
