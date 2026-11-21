<script setup lang="ts">
import { inject, type ComputedRef } from 'vue'
import CatalogBrowserIsland from '@/islands/CatalogBrowserIsland.vue'

const auth = inject<{ isAuthenticated: ComputedRef<boolean> }>('pulsarAuth')
</script>

<template>
  <div class="space-y-4">
    <div>
      <h1 class="text-2xl font-bold tracking-tight">Catalog</h1>
      <p class="text-sm text-base-content/65">Search works and creators across music, anime, books, video, and games</p>
    </div>
    <CatalogBrowserIsland :signed-in="auth?.isAuthenticated.value ?? false" />
  </div>
</template>
