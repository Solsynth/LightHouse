<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Add16, Renew16 } from '@carbon/icons-vue'
import { request, formatDuration, DOMAINS, type ActiveSession, type MediaStats } from '@/lib/api'

const sessions = ref<ActiveSession[]>([])
const stats = ref<MediaStats | null>(null)
const loading = ref(true)
const error = ref('')
const feedback = ref('')

const domain = ref('music')
const title = ref('')
const subtitle = ref('')
const manualId = ref('web-manual')
const durationMinutes = ref<number | undefined>(undefined)
const leaseMinutes = ref(30)
const submitting = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [activeRes, statsRes] = await Promise.all([
      request<{ sessions?: ActiveSession[] } | ActiveSession[]>('/api/sessions/active'),
      request<MediaStats>('/api/activities/stats').catch(() => null),
    ])
    sessions.value = Array.isArray(activeRes) ? activeRes : (activeRes.sessions ?? [])
    stats.value = statsRes
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to load activities'
  } finally {
    loading.value = false
  }
}

async function startSession() {
  if (!title.value.trim()) return
  submitting.value = true
  error.value = ''
  feedback.value = ''
  try {
    await request('/api/sessions/now-playing', {
      method: 'POST',
      body: {
        domain: domain.value,
        manual_id: manualId.value.trim() || 'web-manual',
        title: title.value.trim(),
        subtitle: subtitle.value.trim() || undefined,
        duration_seconds: durationMinutes.value ? durationMinutes.value * 60 : 0,
        lease_minutes: leaseMinutes.value || 30,
      },
    })
    title.value = ''
    subtitle.value = ''
    feedback.value = 'Active session started'
    await load()
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to start session'
  } finally {
    submitting.value = false
  }
}

async function togglePause(s: ActiveSession) {
  try {
    await request('/api/sessions/pause', {
      method: 'POST',
      body: { manual_id: s.manual_id || 'default', is_paused: !s.is_paused },
    })
    await load()
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to update session'
  }
}

async function endSession(s: ActiveSession, autoScrobble: boolean) {
  try {
    await request('/api/sessions/end', {
      method: 'POST',
      body: { manual_id: s.manual_id || 'default', auto_scrobble: autoScrobble },
    })
    feedback.value = autoScrobble ? 'Session ended & scrobbled' : 'Session ended'
    await load()
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to end session'
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <div v-if="error" class="alert alert-error rounded-xl text-sm">{{ error }}</div>
    <div v-if="feedback" class="alert alert-success rounded-xl text-sm">{{ feedback }}</div>

    <div v-if="stats" class="grid grid-cols-2 sm:grid-cols-3 gap-4">
      <div class="rounded-2xl bg-base-200 p-4">
        <div class="text-2xl font-bold font-mono">{{ stats.total_scrobbles }}</div>
        <div class="text-xs text-[var(--console-faint)] uppercase tracking-wider mt-1">Total Scrobbles</div>
      </div>
      <div class="rounded-2xl bg-base-200 p-4">
        <div class="text-2xl font-bold font-mono">{{ formatDuration(stats.total_seconds) }}</div>
        <div class="text-xs text-[var(--console-faint)] uppercase tracking-wider mt-1">Total Time</div>
      </div>
      <div class="rounded-2xl bg-base-200 p-4 col-span-2 sm:col-span-1">
        <div class="text-2xl font-bold font-mono">{{ stats.days_active }}</div>
        <div class="text-xs text-[var(--console-faint)] uppercase tracking-wider mt-1">Active Days</div>
      </div>
    </div>

    <form class="rounded-2xl bg-base-200 p-5 space-y-4" @submit.prevent="startSession">
      <div class="flex items-center justify-between">
        <h2 class="font-semibold">Broadcast Now Playing / Activity</h2>
        <button type="button" class="btn btn-ghost btn-xs rounded-lg" @click="load">
          <Renew16 class="w-3.5 h-3.5" />
          Refresh
        </button>
      </div>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <input v-model="title" type="text" required placeholder="Title (track, anime, game, book)" class="input input-bordered rounded-xl w-full" />
        <select v-model="domain" class="select select-bordered rounded-xl w-full">
          <option v-for="d in DOMAINS" :key="d" :value="d">{{ d }}</option>
        </select>
        <input v-model="subtitle" type="text" placeholder="Artist / Studio / Author (optional)" class="input input-bordered rounded-xl w-full" />
        <div class="grid grid-cols-2 gap-2">
          <input v-model.number="durationMinutes" type="number" min="0" placeholder="Duration (min)" class="input input-bordered rounded-xl w-full" />
          <input v-model.number="leaseMinutes" type="number" min="1" max="240" placeholder="Lease (min)" class="input input-bordered rounded-xl w-full" />
        </div>
      </div>
      <div class="flex justify-end">
        <button type="submit" class="btn btn-primary btn-sm rounded-xl" :disabled="submitting">
          <Add16 class="w-4 h-4" />
          {{ submitting ? 'Starting...' : 'Set Active Session' }}
        </button>
      </div>
    </form>

    <section class="space-y-3">
      <h2 class="text-sm font-mono uppercase tracking-wider text-primary">Active Sessions ({{ sessions.length }})</h2>
      <div v-if="loading" class="skeleton h-20 rounded-2xl"></div>
      <p v-else-if="sessions.length === 0" class="rounded-2xl bg-base-200 p-6 text-sm text-[var(--console-faint)]">
        No active sessions right now.
      </p>
      <div v-else class="space-y-3">
        <div v-for="s in sessions" :key="s.id" class="rounded-2xl bg-base-200 p-4 flex flex-col sm:flex-row sm:items-center justify-between gap-4">
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <span class="badge badge-xs badge-primary uppercase">{{ s.domain }}</span>
              <span v-if="s.is_paused" class="badge badge-xs badge-warning">Paused</span>
              <span class="text-[11px] font-mono text-[var(--console-faint)]">{{ s.manual_id }}</span>
            </div>
            <p class="font-semibold mt-1 truncate">{{ s.title }}</p>
            <p v-if="s.subtitle" class="text-xs text-[var(--console-dim)] truncate">{{ s.subtitle }}</p>
          </div>
          <div class="flex items-center gap-2 shrink-0">
            <button type="button" class="btn btn-ghost btn-xs bg-base-100 rounded-lg" @click="togglePause(s)">
              {{ s.is_paused ? 'Resume' : 'Pause' }}
            </button>
            <button type="button" class="btn btn-ghost btn-xs bg-base-100 rounded-lg" @click="endSession(s, true)">
              Finish &amp; Scrobble
            </button>
            <button type="button" class="btn btn-ghost btn-xs text-error bg-base-100 rounded-lg" @click="endSession(s, false)">
              End
            </button>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>
