<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { request, formatDuration, type MediaStats } from '@/lib/api'

const stats = ref<MediaStats | null>(null)
const loading = ref(true)

onMounted(async () => {
  try {
    stats.value = await request<MediaStats>('/api/stats')
  } catch {
    stats.value = null
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <section class="rounded-2xl bg-base-200 p-5 space-y-4">
    <h2 class="text-sm font-mono uppercase tracking-wider text-[var(--console-faint)]">Your Stats</h2>
    <div v-if="loading" class="skeleton h-36 w-full rounded-xl"></div>
    <div v-else-if="stats" class="space-y-4">
      <div class="grid grid-cols-2 gap-3">
        <div class="rounded-xl bg-base-100 p-3">
          <div class="text-xl font-bold font-mono">{{ stats.total_scrobbles }}</div>
          <div class="text-[11px] uppercase tracking-wider text-[var(--console-faint)]">Scrobbles</div>
        </div>
        <div class="rounded-xl bg-base-100 p-3">
          <div class="text-xl font-bold font-mono">{{ formatDuration(stats.total_seconds) }}</div>
          <div class="text-[11px] uppercase tracking-wider text-[var(--console-faint)]">Time</div>
        </div>
      </div>

      <div v-if="Object.keys(stats.domain_breakdown || {}).length" class="space-y-2">
        <p class="text-xs font-mono uppercase tracking-wider text-[var(--console-faint)]">By Domain</p>
        <div class="space-y-1.5">
          <div
            v-for="(count, domain) in stats.domain_breakdown"
            :key="domain"
            class="flex items-center justify-between rounded-lg bg-base-100 px-3 py-1.5 text-xs"
          >
            <span class="capitalize">{{ domain }}</span>
            <span class="font-mono text-primary">{{ count }}</span>
          </div>
        </div>
      </div>

      <div v-if="stats.top_items?.length" class="space-y-2">
        <p class="text-xs font-mono uppercase tracking-wider text-[var(--console-faint)]">Top Items</p>
        <div class="space-y-1.5">
          <div
            v-for="item in stats.top_items.slice(0, 5)"
            :key="item.name"
            class="flex items-center justify-between gap-2 text-xs"
          >
            <span class="truncate">{{ item.name }}</span>
            <span class="font-mono text-[var(--console-faint)] shrink-0">{{ item.count }}×</span>
          </div>
        </div>
      </div>
    </div>
    <p v-else class="text-sm text-[var(--console-faint)]">Unable to load stats.</p>
  </section>
</template>
