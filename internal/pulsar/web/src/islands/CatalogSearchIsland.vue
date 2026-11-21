<script setup lang="ts">
import { ref } from 'vue'
import { Search16 } from '@carbon/icons-vue'
import { request, DOMAINS, type UnifiedMedia } from '@/lib/api'

const query = ref('')
const domain = ref<string>('music')
const results = ref<UnifiedMedia[]>([])
const loading = ref(false)
const searched = ref(false)
const error = ref('')

async function doSearch() {
  const q = query.value.trim()
  if (!q) return
  loading.value = true
  error.value = ''
  searched.value = true
  try {
    const res = await request<UnifiedMedia[] | { items?: UnifiedMedia[] }>('/api/metadata/search', {
      query: { q, domain: domain.value },
    })
    results.value = Array.isArray(res) ? res : (res.items ?? [])
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Search failed'
    results.value = []
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="rounded-2xl bg-base-200 p-5 space-y-4">
    <form class="flex flex-col sm:flex-row gap-2" @submit.prevent="doSearch">
      <div class="join flex-1">
        <input
          v-model="query"
          type="search"
          placeholder="Search tracks, anime, games, books, films…"
          class="input input-bordered join-item w-full bg-base-100 rounded-l-xl"
        />
        <button type="submit" class="btn btn-primary join-item rounded-r-xl" :disabled="loading">
          <Search16 class="h-4 w-4" />
          <span class="hidden sm:inline">Search</span>
        </button>
      </div>
      <select v-model="domain" class="select select-bordered bg-base-100 rounded-xl capitalize">
        <option v-for="d in DOMAINS" :key="d" :value="d">{{ d }}</option>
      </select>
    </form>

    <div v-if="loading" class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
      <div v-for="n in 3" :key="n" class="skeleton h-20 rounded-xl"></div>
    </div>

    <p v-else-if="error" class="text-sm text-error">{{ error }}</p>

    <p v-else-if="searched && results.length === 0" class="text-sm text-[var(--console-faint)]">
      No matching items found in {{ domain }}.
    </p>

    <div v-else-if="results.length" class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
      <a
        v-for="item in results"
        :key="item.urn"
        :href="`/m/${encodeURIComponent(item.urn)}`"
        class="rounded-xl bg-base-100 p-3 flex items-center gap-3 hover:bg-base-300/70 transition-colors"
      >
        <img
          v-if="item.artwork_url"
          :src="item.artwork_url"
          :alt="item.title"
          class="h-12 w-12 rounded-lg object-cover bg-base-300 shrink-0"
        />
        <div
          v-else
          class="h-12 w-12 rounded-lg bg-base-300 flex items-center justify-center text-[10px] font-mono text-[var(--console-faint)] shrink-0"
        >
          {{ item.domain }}
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-semibold truncate">{{ item.title }}</p>
          <p v-if="item.subtitle" class="text-xs text-[var(--console-dim)] truncate">{{ item.subtitle }}</p>
          <p class="text-[11px] font-mono text-[var(--console-faint)] truncate">{{ item.provider }}</p>
        </div>
      </a>
    </div>
  </div>
</template>
