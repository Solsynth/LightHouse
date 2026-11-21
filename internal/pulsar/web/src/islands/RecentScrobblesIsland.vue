<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { requestList, formatTimestamp, type Scrobble } from '@/lib/api'

const props = withDefaults(defineProps<{ limit?: number }>(), { limit: 10 })

const items = ref<Scrobble[]>([])
const loading = ref(true)

onMounted(async () => {
  try {
    const res = await requestList<Scrobble>('/api/history', { take: props.limit, offset: 0 })
    items.value = res.items
  } catch {
    items.value = []
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <section class="rounded-2xl bg-base-200 p-5 space-y-4">
    <div class="flex items-center justify-between">
      <h2 class="text-sm font-mono uppercase tracking-wider text-[var(--console-faint)]">Recent Scrobbles</h2>
      <a href="/history" class="text-xs text-primary hover:underline">View all</a>
    </div>

    <div v-if="loading" class="space-y-2">
      <div v-for="n in 4" :key="n" class="skeleton h-12 w-full rounded-xl"></div>
    </div>
    <p v-else-if="items.length === 0" class="text-sm text-[var(--console-faint)]">
      No scrobbles yet. Start playing music, anime, or games with a connected client.
    </p>
    <div v-else class="divide-y divide-[var(--console-line-soft)]">
      <div v-for="s in items" :key="s.id" class="py-2.5 flex items-center gap-3 first:pt-0 last:pb-0">
        <img
          v-if="s.artwork_url"
          :src="s.artwork_url"
          :alt="s.title"
          class="h-10 w-10 rounded-lg object-cover bg-base-300 shrink-0"
        />
        <div
          v-else
          class="h-10 w-10 rounded-lg bg-base-300 flex items-center justify-center text-[10px] font-mono text-[var(--console-faint)] shrink-0"
        >
          {{ s.domain }}
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium truncate">{{ s.title }}</p>
          <p v-if="s.subtitle" class="text-xs text-[var(--console-dim)] truncate">{{ s.subtitle }}</p>
        </div>
        <div class="text-right shrink-0">
          <span class="badge badge-xs badge-ghost uppercase">{{ s.domain }}</span>
          <span class="text-[11px] font-mono text-[var(--console-faint)]">{{ formatTimestamp(s.played_at) }}</span>
        </div>
      </div>
    </div>
  </section>
</template>
