<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Add16, TrashCan16, Link16 } from '@carbon/icons-vue'
import { request, requestList, formatDuration, formatTimestamp, DOMAINS, type Scrobble, type UnifiedMedia } from '@/lib/api'

const items = ref<Scrobble[]>([])
const total = ref(0)
const domain = ref('')
const offset = ref(0)
const take = 25
const loading = ref(true)
const error = ref('')

const showLog = ref(false)
const logDomain = ref('music')
const logTitle = ref('')
const logSubtitle = ref('')
const logCaption = ref('')
const logDuration = ref<number | undefined>(undefined)
const logging = ref(false)

const linkingScrobble = ref<Scrobble | null>(null)
const linkQuery = ref('')
const linkResults = ref<UnifiedMedia[]>([])
const linking = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const res = await requestList<Scrobble>('/api/history', {
      domain: domain.value || undefined,
      offset: offset.value,
      take,
    })
    items.value = res.items
    total.value = res.total
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to load history'
  } finally {
    loading.value = false
  }
}

async function remove(id: string) {
  try {
    await request(`/api/scrobbles/${id}`, { method: 'DELETE' })
    items.value = items.value.filter((i) => i.id !== id)
    total.value = Math.max(0, total.value - 1)
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Delete failed'
  }
}

function openLinkModal(s: Scrobble) {
  linkingScrobble.value = s
  linkQuery.value = s.subtitle ? `${s.title} ${s.subtitle}` : s.title
  linkResults.value = []
  void searchLinkTargets()
}

async function searchLinkTargets() {
  if (!linkingScrobble.value || !linkQuery.value.trim()) return
  linking.value = true
  try {
    const res = await request<UnifiedMedia[] | { items?: UnifiedMedia[] }>('/api/metadata/search', {
      query: { q: linkQuery.value.trim(), domain: linkingScrobble.value.domain },
    })
    linkResults.value = Array.isArray(res) ? res : (res.items ?? [])
  } catch {
    linkResults.value = []
  } finally {
    linking.value = false
  }
}

async function applyLink(urn: string) {
  if (!linkingScrobble.value) return
  try {
    await request(`/api/scrobbles/${linkingScrobble.value.id}/link`, {
      method: 'PUT',
      body: { urn },
    })
    linkingScrobble.value = null
    await load()
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to link scrobble'
  }
}

function setDomain(d: string) {
  domain.value = d
  offset.value = 0
  void load()
}

function prevPage() {
  if (offset.value <= 0) return
  offset.value = Math.max(0, offset.value - take)
  void load()
}

function nextPage() {
  if (offset.value + take >= total.value) return
  offset.value += take
  void load()
}

async function submitManualScrobble() {
  if (!logTitle.value.trim()) return
  logging.value = true
  error.value = ''
  try {
    await request('/api/scrobbles', {
      method: 'POST',
      body: {
        scrobbles: [
          {
            domain: logDomain.value,
            client_name: 'pulsar-web',
            title: logTitle.value.trim(),
            subtitle: logSubtitle.value.trim() || undefined,
            caption: logCaption.value.trim() || undefined,
            listened_seconds: logDuration.value || 180,
            duration_seconds: logDuration.value || 180,
          },
        ],
      },
    })
    logTitle.value = ''
    logSubtitle.value = ''
    logCaption.value = ''
    logDuration.value = undefined
    showLog.value = false
    offset.value = 0
    await load()
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to log scrobble'
  } finally {
    logging.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
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
      <div class="flex items-center gap-3">
        <span class="text-xs font-mono text-[var(--console-faint)]">{{ total }} total</span>
        <button type="button" class="btn btn-primary btn-xs rounded-lg" @click="showLog = !showLog">
          <Add16 class="w-3.5 h-3.5" />
          Log Scrobble
        </button>
      </div>
    </div>

    <form v-if="showLog" class="rounded-2xl bg-base-200 p-4 space-y-3" @submit.prevent="submitManualScrobble">
      <h3 class="text-sm font-semibold">Manual Scrobble</h3>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <input v-model="logTitle" type="text" required placeholder="Title" class="input input-bordered input-sm rounded-xl w-full" />
        <select v-model="logDomain" class="select select-bordered select-sm rounded-xl w-full">
          <option v-for="d in DOMAINS" :key="d" :value="d">{{ d }}</option>
        </select>
        <input v-model="logSubtitle" type="text" placeholder="Artist / Studio / Author" class="input input-bordered input-sm rounded-xl w-full" />
        <input v-model="logCaption" type="text" placeholder="Album / Series / Platform (optional)" class="input input-bordered input-sm rounded-xl w-full" />
        <input v-model.number="logDuration" type="number" min="1" placeholder="Duration in seconds (default 180)" class="input input-bordered input-sm rounded-xl w-full" />
      </div>
      <div class="flex justify-end gap-2">
        <button type="button" class="btn btn-ghost btn-xs rounded-lg" @click="showLog = false">Cancel</button>
        <button type="submit" class="btn btn-primary btn-xs rounded-lg" :disabled="logging">
          {{ logging ? 'Logging...' : 'Submit Scrobble' }}
        </button>
      </div>
    </form>

    <div v-if="error" class="alert alert-error rounded-xl text-sm">{{ error }}</div>

    <div v-if="loading" class="space-y-2">
      <div v-for="n in 6" :key="n" class="skeleton h-14 rounded-xl"></div>
    </div>

    <div v-else-if="items.length === 0" class="rounded-2xl bg-base-200 p-8 text-center text-sm text-[var(--console-faint)]">
      No scrobbles recorded for this filter.
    </div>

    <div v-else class="rounded-2xl bg-base-200 divide-y divide-[var(--console-line-soft)]">
      <div v-for="s in items" :key="s.id" class="p-4 flex items-center gap-3.5">
        <img
          v-if="s.artwork_url"
          :src="s.artwork_url"
          :alt="s.title"
          class="h-11 w-11 rounded-lg object-cover bg-base-300 shrink-0"
        />
        <div
          v-else
          class="h-11 w-11 rounded-lg bg-base-300 flex items-center justify-center text-[10px] font-mono text-[var(--console-faint)] shrink-0"
        >
          {{ s.domain }}
        </div>
        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-2">
            <p class="font-semibold text-sm truncate">{{ s.title }}</p>
            <span v-if="s.catalog_id" class="badge badge-xs badge-success">Linked</span>
          </div>
          <p class="text-xs text-[var(--console-dim)] truncate">
            <span v-if="s.subtitle">{{ s.subtitle }} · </span>
            <span class="font-mono">{{ formatDuration(s.listened_seconds) }}</span>
            <span> · {{ s.client_name }}</span>
          </p>
        </div>
        <span class="hidden sm:inline text-xs font-mono text-[var(--console-faint)] shrink-0">
          {{ formatTimestamp(s.played_at) }}
        </span>
        <div class="flex items-center gap-1 shrink-0">
          <button
            type="button"
            class="btn btn-ghost btn-xs btn-square rounded-lg"
            title="Link to catalog"
            @click="openLinkModal(s)"
          >
            <Link16 class="h-4 w-4" />
          </button>
          <button
            type="button"
            class="btn btn-ghost btn-xs btn-square rounded-lg text-error"
            title="Delete scrobble"
            @click="remove(s.id)"
          >
            <TrashCan16 class="h-4 w-4" />
          </button>
        </div>
      </div>
    </div>

    <div v-if="total > take" class="flex items-center justify-between pt-2">
      <button type="button" class="btn btn-sm btn-ghost rounded-xl" :disabled="offset === 0" @click="prevPage">
        Previous
      </button>
      <span class="text-xs font-mono text-[var(--console-faint)]">
        {{ offset + 1 }}–{{ Math.min(offset + take, total) }} of {{ total }}
      </span>
      <button type="button" class="btn btn-sm btn-ghost rounded-xl" :disabled="offset + take >= total" @click="nextPage">
        Next
      </button>
    </div>

    <dialog v-if="linkingScrobble" open class="modal modal-open">
      <div class="modal-box rounded-2xl bg-base-200 max-w-lg space-y-4">
        <h3 class="font-bold text-lg">Link Scrobble to Catalog</h3>
        <p class="text-xs text-[var(--console-dim)] truncate">
          {{ linkingScrobble.title }} <span v-if="linkingScrobble.subtitle">· {{ linkingScrobble.subtitle }}</span>
        </p>
        <form class="flex gap-2" @submit.prevent="searchLinkTargets">
          <input v-model="linkQuery" type="text" class="input input-bordered input-sm flex-1 bg-base-100 rounded-xl" />
          <button type="submit" class="btn btn-primary btn-sm rounded-xl" :disabled="linking">Search</button>
        </form>
        <div v-if="linking" class="skeleton h-20 rounded-xl"></div>
        <div v-else class="max-h-64 overflow-y-auto space-y-2">
          <button
            v-for="m in linkResults"
            :key="m.urn"
            type="button"
            class="w-full text-left rounded-xl bg-base-100 p-3 hover:bg-base-300 transition-colors flex items-center gap-3"
            @click="applyLink(m.urn)"
          >
            <img v-if="m.artwork_url" :src="m.artwork_url" class="h-10 w-10 rounded-lg object-cover shrink-0" />
            <div class="min-w-0 flex-1">
              <p class="text-sm font-medium truncate">{{ m.title }}</p>
              <p class="text-xs text-[var(--console-dim)] truncate">{{ m.subtitle }} · {{ m.provider }}</p>
            </div>
          </button>
        </div>
        <div class="modal-action">
          <button type="button" class="btn btn-sm btn-ghost rounded-xl" @click="linkingScrobble = null">Close</button>
        </div>
      </div>
    </dialog>
  </div>
</template>
