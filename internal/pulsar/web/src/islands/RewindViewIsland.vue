<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { request, formatDuration, type MediaStats } from '@/lib/api'

const props = defineProps<{ year: number; identifier?: string; personal?: boolean }>()

const stats = ref<MediaStats | null>(null)
const loading = ref(true)
const error = ref('')

onMounted(async () => {
  try {
    const endpoint = props.personal || !props.identifier
      ? `/api/rewind/${props.year}`
      : `/api/users/${encodeURIComponent(props.identifier)}/rewind/${props.year}`
    const res = await request<{ rewind: MediaStats }>(endpoint)
    stats.value = res.rewind
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Unable to load Rewind'
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div>
    <div v-if="loading" class="skeleton h-64 w-full rounded-2xl"></div>
    <div v-else-if="error" class="alert alert-error rounded-2xl text-sm">{{ error }}</div>
    <div v-else-if="stats" class="space-y-6">
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-4">
        <div class="rounded-2xl bg-base-200 p-6">
          <div class="text-3xl font-bold font-mono text-primary">{{ stats.total_scrobbles }}</div>
          <div class="text-xs uppercase tracking-wider text-[var(--console-faint)] mt-1">Total Scrobbles</div>
        </div>
        <div class="rounded-2xl bg-base-200 p-6">
          <div class="text-3xl font-bold font-mono">{{ formatDuration(stats.total_seconds) }}</div>
          <div class="text-xs uppercase tracking-wider text-[var(--console-faint)] mt-1">Time Spent</div>
        </div>
        <div class="rounded-2xl bg-base-200 p-6">
          <div class="text-3xl font-bold font-mono">{{ stats.days_active }}</div>
          <div class="text-xs uppercase tracking-wider text-[var(--console-faint)] mt-1">Active Days</div>
        </div>
      </div>

      <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
        <section class="rounded-2xl bg-base-200 p-5 space-y-3">
          <h2 class="text-sm font-mono uppercase tracking-wider text-[var(--console-faint)]">Top Items</h2>
          <p v-if="!stats.top_items?.length" class="text-sm text-[var(--console-faint)]">No top items recorded.</p>
          <div v-else class="space-y-2.5">
            <div v-for="(item, idx) in stats.top_items.slice(0, 10)" :key="item.name" class="flex items-center gap-3">
              <span class="w-5 text-xs font-mono text-[var(--console-faint)]">{{ idx + 1 }}</span>
              <div class="min-w-0 flex-1">
                <p class="text-sm font-medium truncate">{{ item.name }}</p>
                <p v-if="item.subtitle" class="text-xs text-[var(--console-dim)] truncate">{{ item.subtitle }}</p>
              </div>
              <span class="text-xs font-mono text-primary">{{ item.count }}×</span>
            </div>
          </div>
        </section>

        <section class="rounded-2xl bg-base-200 p-5 space-y-3">
          <h2 class="text-sm font-mono uppercase tracking-wider text-[var(--console-faint)]">Top Creators</h2>
          <p v-if="!stats.top_creators?.length" class="text-sm text-[var(--console-faint)]">No top creators recorded.</p>
          <div v-else class="space-y-2.5">
            <div v-for="(c, idx) in stats.top_creators.slice(0, 10)" :key="c.name" class="flex items-center gap-3">
              <span class="w-5 text-xs font-mono text-[var(--console-faint)]">{{ idx + 1 }}</span>
              <p class="text-sm font-medium truncate flex-1">{{ c.name }}</p>
              <span class="text-xs font-mono text-primary">{{ c.count }}×</span>
            </div>
          </div>
        </section>
      </div>
    </div>
  </div>
</template>
