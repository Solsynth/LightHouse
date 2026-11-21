<script setup lang="ts">
import HistoryManagerIsland from '@/islands/HistoryManagerIsland.vue'
</script>

<template>
  <div class="space-y-4">
    <div>
      <h1 class="text-2xl font-bold tracking-tight">Scrobble History</h1>
      <p class="text-sm text-base-content/65">Log manual scrobbles, filter by domain, or remove entries</p>
    </div>
    <HistoryManagerIsland />
  </div>
</template>
