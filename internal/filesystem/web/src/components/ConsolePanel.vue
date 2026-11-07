<script setup lang="ts">
defineProps<{
  title: string
  note?: string
}>()
</script>

<template>
  <section class="rounded-card bg-base-300/35">
    <header class="flex flex-wrap items-baseline justify-between gap-2 px-5 pb-1 pt-4">
      <h2 class="eyebrow text-dim">{{ title }}</h2>
      <span v-if="note" class="text-2xs text-faint">{{ note }}</span>
      <slot name="header" />
    </header>
    <div class="px-5 py-4">
      <slot />
    </div>
    <footer v-if="$slots.footer" class="px-5 pb-3 pt-1">
      <slot name="footer" />
    </footer>
  </section>
</template>
