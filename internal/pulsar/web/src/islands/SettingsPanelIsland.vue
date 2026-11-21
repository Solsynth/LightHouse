<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Renew16 } from '@carbon/icons-vue'
import { request, formatTimestamp, type BangumiConnection } from '@/lib/api'

const conn = ref<BangumiConnection | null>(null)
const loading = ref(true)
const syncing = ref(false)
const saving = ref(false)
const message = ref('')
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    conn.value = await request<BangumiConnection>('/api/bangumi/connection')
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Unable to load Bangumi connection'
  } finally {
    loading.value = false
  }
}

async function saveSettings() {
  if (!conn.value) return
  saving.value = true
  message.value = ''
  error.value = ''
  try {
    conn.value = await request<BangumiConnection>('/api/bangumi/connection', {
      method: 'PATCH',
      body: {
        sync_enabled: conn.value.sync_enabled,
        conflict_policy: conn.value.conflict_policy,
      },
    })
    message.value = 'Bangumi sync settings updated.'
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to update settings'
  } finally {
    saving.value = false
  }
}

async function triggerSync() {
  syncing.value = true
  message.value = ''
  error.value = ''
  try {
    await request('/api/bangumi/sync', { method: 'POST' })
    message.value = 'Bangumi sync completed.'
    await load()
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Sync failed'
  } finally {
    syncing.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-6">
    <div v-if="message" class="alert alert-success rounded-xl text-sm">{{ message }}</div>
    <div v-if="error" class="alert alert-error rounded-xl text-sm">{{ error }}</div>

    <section class="rounded-2xl bg-base-200 p-6 space-y-4">
      <div class="flex items-center justify-between gap-4">
        <div>
          <h2 class="text-base font-semibold">Bangumi Collection Sync</h2>
          <p class="text-xs text-[var(--console-dim)]">Reconcile your anime, book, and game watchlists with bgm.tv.</p>
        </div>
        <span v-if="conn" class="badge badge-sm" :class="conn.linked ? 'badge-success' : 'badge-ghost'">
          {{ conn.linked ? 'Linked' : 'Not Linked' }}
        </span>
      </div>

      <div v-if="loading" class="skeleton h-28 rounded-xl"></div>
      <div v-else-if="!conn?.linked" class="rounded-xl bg-base-100 p-4 text-sm text-[var(--console-dim)]">
        Bangumi is not linked to your Solarpass account yet. Link it in your Stargate account connections to enable sync.
      </div>
      <div v-else class="space-y-4">
        <div class="rounded-xl bg-base-100 p-4 flex flex-wrap items-center justify-between gap-4">
          <div>
            <p class="text-sm font-medium">{{ conn.remote_username || conn.remote_user_id }}</p>
            <p v-if="conn.last_synced_at" class="text-xs font-mono text-[var(--console-faint)]">
              Last synced: {{ formatTimestamp(conn.last_synced_at) }}
            </p>
            <p v-if="conn.last_error" class="text-xs text-error mt-1">{{ conn.last_error }}</p>
          </div>
          <button type="button" class="btn btn-sm btn-primary rounded-xl" :disabled="syncing" @click="triggerSync">
            <Renew16 class="h-4 w-4" :class="{ 'animate-spin': syncing }" />
            {{ syncing ? 'Syncing…' : 'Sync Now' }}
          </button>
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <label class="rounded-xl bg-base-100 p-4 flex items-center justify-between cursor-pointer">
            <span class="text-sm font-medium">Background Sync</span>
            <input v-model="conn.sync_enabled" type="checkbox" class="toggle toggle-primary" @change="saveSettings" />
          </label>
          <div class="rounded-xl bg-base-100 p-4 flex items-center justify-between gap-3">
            <span class="text-sm font-medium">Conflict Policy</span>
            <select
              v-model="conn.conflict_policy"
              class="select select-bordered select-sm bg-base-200 rounded-xl"
              :disabled="saving"
              @change="saveSettings"
            >
              <option value="remote">Remote (Bangumi)</option>
              <option value="local">Local (Pulsar)</option>
              <option value="newest">Newest</option>
            </select>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>
