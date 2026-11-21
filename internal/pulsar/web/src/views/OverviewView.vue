<script setup lang="ts">
import { RouterLink } from 'vue-router'
import NowPlayingIsland from '@/islands/NowPlayingIsland.vue'
import QuickStatsIsland from '@/islands/QuickStatsIsland.vue'
import RecentScrobblesIsland from '@/islands/RecentScrobblesIsland.vue'

const currentYear = new Date().getFullYear()
</script>

<template>
  <div class="space-y-6">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h1 class="text-2xl font-bold tracking-tight">Overview</h1>
        <p class="text-sm text-base-content/65">Live playback, stats, and recent scrobbles</p>
      </div>
      <RouterLink :to="`/rewind/${currentYear}`" class="btn btn-primary btn-sm">
        {{ currentYear }} Rewind
      </RouterLink>
    </div>

    <NowPlayingIsland />
    <QuickStatsIsland />

    <section class="space-y-3">
      <div class="flex items-center justify-between">
        <h2 class="text-sm font-semibold uppercase tracking-wider text-base-content/60">
          Recent Scrobbles
        </h2>
        <RouterLink to="/history" class="text-xs text-primary hover:underline">
          Manage all →
        </RouterLink>
      </div>
      <RecentScrobblesIsland :limit="12" />
    </section>
  </div>
</template>
