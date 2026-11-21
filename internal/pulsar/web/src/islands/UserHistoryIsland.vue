<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { requestList, formatDuration, formatTimestamp, DOMAINS, type Scrobble } from '@/lib/api'

const props = defineProps<{ identifier: string }>()

const items = ref<Scrobble[]>([])
const total = ref(0)
const domain = ref('')
const offset = ref(0)
const take = 15
const loading = ref(true)

async function load() {
  loading.value = true
  try {
    const res = await requestList<Scrobble>(`/api/users/${encodeURIComponent(props.identifier)}/history`, {
      domain: domain.value || undefined,
      offset: offset.value,
      take,
    })
    items.value = res.items
    total.value = res.total
  } catch {
    items.value = []
  } finally {
    loading.value = false
  }
}

function setDomain(d: string) {
  domain.value = d
  offset.value = 0
  void load()
}

onMounted(load)
</script>

<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div class="flex flex-wrap gap-1.5">
        <button
          type="button"
          class="btn btn-xs rounded-lg"
          :class="domain === '' ? 'btn-primary' : 'btn-ghost bg-base-200'"
          @click="setDomain('')"
        >
          All
        </button>
        <button
          v-for="d in DOMAINS"
          :key="d"
          type="button"
          class="btn btn-xs rounded-lg capitalize"
          :class="domain === d ? 'btn-primary' : 'btn-ghost bg-base-200'"
          @click="setDomain(d)"
        >
          {{ d }}
        </button>
      </div>
      <span class="text-xs font-mono text-[var(--console-faint)]">{{ total }} scrobbles</span>
    </div>

    <div v-if="loading" class="space-y-2">
      <div v-for="n in 4" :key="n" class="skeleton h-12 rounded-xl"></div>
    </div>
    <p v-else-if="items.length === 0" class="text-sm text-[var(--console-faint)]">No scrobbles found.</p>
    <div v-else class="rounded-2xl bg-base-200 divide-y divide-[var(--console-line-soft)]">
      <div v-for="s in items" :key="s.id" class="p-3.5 flex items-center gap-3">
        <img v-if="s.artwork_url" :src="s.artwork_url" class="h-10 w-10 rounded-lg object-cover bg-base-300 shrink-0" />
        <div v-else class="h-10 w-10 rounded-lg bg-base-300 flex items-center justify-center text-[10px] font-mono text-[var(--console-faint)] shrink-0">
          {{ s.domain }}
        </div>
        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-2">
            <span class="chip capitalize">{{ s.domain }}</span>
            <p class="text-sm font-medium truncate">{{ s.title }}</p>
          </div>
          <p class="text-xs text-[var(--console-dim)] truncate mt-0.5">
            <span v-if="s.subtitle">{{ s.subtitle }} · </span>
            <span class="font-mono">{{ formatDuration(s.listened_seconds) }}</span>
          </p>
        </div>
        <span class="text-xs font-mono text-[var(--console-faint)] shrink-0">{{ formatTimestamp(s.played_at) }}</span>
      </div>
    </div>

    <div v-if="total > take" class="flex items-center justify-between">
      <button type="button" class="btn btn-xs btn-ghost rounded-lg" :disabled="offset === 0" @click="offset = Math.max(0, offset - take); load()">
        Previous
      </button>
      <button type="button" class="btn btn-xs btn-ghost rounded-lg" :disabled="offset + take >= total" @click="offset += take; load()">
        Next
      </button>
    </div>
  </div>
</template>
