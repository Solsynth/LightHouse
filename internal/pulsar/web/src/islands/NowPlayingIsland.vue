<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { request, formatDuration, type ActiveSession } from '@/lib/api'

const sessions = ref<ActiveSession[]>([])
const loading = ref(true)
let timer: number | undefined

async function load() {
  try {
    const res = await request<{ sessions?: ActiveSession[] } | ActiveSession[]>('/api/sessions/active')
    sessions.value = Array.isArray(res) ? res : (res.sessions ?? [])
  } catch {
    sessions.value = []
  } finally {
    loading.value = false
  }
}

async function togglePause(s: ActiveSession) {
  try {
    await request('/api/sessions/pause', {
      method: 'POST',
      body: { manual_id: s.manual_id || 'default', is_paused: !s.is_paused },
    })
    await load()
  } catch {
    // ignore
  }
}

async function endSession(s: ActiveSession) {
  try {
    await request('/api/sessions/end', {
      method: 'POST',
      body: { manual_id: s.manual_id || 'default', auto_scrobble: true },
    })
    await load()
  } catch {
    // ignore
  }
}

onMounted(() => {
  load()
  timer = window.setInterval(load, 15000)
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>

<template>
  <section class="rounded-2xl bg-base-200 p-5 space-y-4">
    <div class="flex items-center justify-between">
      <h2 class="text-sm font-mono uppercase tracking-wider text-primary">Active Now</h2>
      <span v-if="!loading && sessions.length" class="badge badge-primary badge-xs">Live</span>
    </div>

    <div v-if="loading" class="skeleton h-16 w-full rounded-xl"></div>
    <p v-else-if="sessions.length === 0" class="text-sm text-[var(--console-faint)]">
      No active playback sessions right now.
    </p>
    <div v-else class="space-y-3">
      <div
        v-for="s in sessions"
        :key="s.id"
        class="rounded-xl bg-base-100 p-3.5 flex items-center gap-3.5"
      >
        <img
          v-if="s.artwork_url"
          :src="s.artwork_url"
          :alt="s.title"
          class="h-12 w-12 rounded-lg object-cover bg-base-300 shrink-0"
        />
        <div
          v-else
          class="h-12 w-12 rounded-lg bg-base-300 flex items-center justify-center text-[11px] font-mono text-[var(--console-faint)] shrink-0"
        >
          {{ s.domain }}
        </div>
        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-2">
            <span class="badge badge-xs badge-ghost uppercase">{{ s.domain }}</span>
            <span v-if="s.is_paused" class="text-[11px] text-warning">Paused</span>
            <span v-if="s.client_name" class="text-[11px] text-[var(--console-faint)] truncate">
              · {{ s.client_name }}
            </span>
          </div>
          <p class="font-semibold text-sm truncate mt-0.5">{{ s.title }}</p>
          <p v-if="s.subtitle" class="text-xs text-[var(--console-dim)] truncate">{{ s.subtitle }}</p>
          <div v-if="s.duration_seconds > 0" class="mt-2 flex items-center gap-2">
            <progress
              class="progress progress-primary h-1.5 flex-1"
              :value="s.progress_seconds"
              :max="s.duration_seconds"
            ></progress>
            <span class="text-[11px] font-mono text-[var(--console-faint)]">
              {{ formatDuration(s.progress_seconds) }} / {{ formatDuration(s.duration_seconds) }}
            </span>
          </div>
        </div>
        <div class="flex flex-col gap-1 shrink-0">
          <button type="button" class="btn btn-ghost btn-xs rounded-lg" @click="togglePause(s)">
            {{ s.is_paused ? 'Resume' : 'Pause' }}
          </button>
          <button type="button" class="btn btn-ghost btn-xs text-error rounded-lg" @click="endSession(s)">
            End
          </button>
        </div>
      </div>
    </div>
  </section>
</template>
