<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import { useNoticeStore } from '@/stores/notice'
import { activityApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { ActionLog } from '@/lib/api/types'
import { describeAgent, place, shortId, utcStamp } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'

const notice = useNoticeStore()

const logs = ref<ActionLog[]>([])
const total = ref(0)
const loading = ref(true)
const error = ref('')
const filter = ref('')
const page = ref(0)
const pageSize = 50

const actions = computed(() => {
  const unique = new Set<string>()
  for (const log of logs.value) unique.add(log.action)
  return [...unique].sort()
})

const offset = computed(() => page.value * pageSize)
const lastPage = computed(() => Math.max(0, Math.ceil(total.value / pageSize) - 1))

async function load() {
  loading.value = true
  error.value = ''
  try {
    const result = await activityApi.list({
      action: filter.value || undefined,
      take: pageSize,
      offset: offset.value,
    })
    logs.value = result.items
    total.value = result.total
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : 'The activity log could not be read.'
  } finally {
    loading.value = false
  }
}

function setFilter(action: string) {
  filter.value = filter.value === action ? '' : action
  page.value = 0
  void load()
}

function go(next: number) {
  page.value = Math.min(Math.max(0, next), lastPage.value)
  void load()
}

function metaSummary(meta?: Record<string, unknown>): string {
  if (!meta) return ''
  const entries = Object.entries(meta)
    .filter(([, value]) => value !== null && value !== undefined && value !== '')
    .slice(0, 2)
  return entries
    .map(([key, value]) => `${key}=${Array.isArray(value) ? value.join(',') : String(value)}`)
    .join(' ')
}

function copyRow(log: ActionLog) {
  const line = `${utcStamp(log.created_at)} ${log.action} ${shortId(log.id)} ${log.ip_address ?? ''}`
  void navigator.clipboard.writeText(line).then(() => notice.ok('Row copied.'))
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      title="Activity"
      description="Every security-relevant action this account took, newest first. Entries record the client, address, and session that performed them."
    >
      <template #actions>
        <button type="button" class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
          Refresh
        </button>
      </template>
    </PageHeader>

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <div v-if="actions.length" class="mb-4 flex flex-wrap items-center gap-2">
      <span class="text-2xs uppercase tracking-[0.12em] text-faint">filter</span>
      <button
        v-for="action in actions"
        :key="action"
        type="button"
        class="chip transition-colors"
        :class="filter === action ? 'border-primary/60 text-base-content' : 'hover:text-base-content'"
        @click="setFilter(action)"
      >
        {{ action }}
      </button>
      <button
        v-if="filter"
        type="button"
        class="text-2xs text-dim underline decoration-line underline-offset-4"
        @click="setFilter(filter)"
      >
        Clear
      </button>
    </div>

    <ConsolePanel title="Log" :note="`${total} entries`">
      <div v-if="loading" class="py-6 text-xs text-faint">Reading action log…</div>

      <p v-else-if="!logs.length" class="voice py-6 text-[12px]">
        Nothing recorded yet. Sign-ins, factor changes, and session revocations appear here as
        they happen.
      </p>

      <div v-else class="overflow-x-auto">
        <table class="ledger">
          <thead>
            <tr>
              <th scope="col">time</th>
              <th scope="col">action</th>
              <th scope="col">detail</th>
              <th scope="col">origin</th>
              <th scope="col">client</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="log in logs"
              :key="log.id"
              class="cursor-pointer"
              @click="copyRow(log)"
            >
              <td class="whitespace-nowrap text-xs tabular-nums text-dim">
                {{ utcStamp(log.created_at).slice(0, 19).replace('T', ' ') }}
              </td>
              <td class="whitespace-nowrap text-xs text-base-content">{{ log.action }}</td>
              <td class="max-w-[280px] truncate text-2xs text-faint" :title="metaSummary(log.meta)">
                {{ metaSummary(log.meta) || '—' }}
              </td>
              <td class="whitespace-nowrap text-xs text-dim">
                {{ log.ip_address || '—' }}
                <p class="text-2xs text-faint">{{ place(log.location) }}</p>
              </td>
              <td class="text-xs text-dim">{{ describeAgent(log.user_agent) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <template #footer>
        <div class="flex items-center justify-between text-2xs text-faint">
          <span>Click a row to copy its timestamp and id.</span>
          <div class="flex items-center gap-3">
            <span class="tabular-nums">{{ offset + 1 }}–{{ Math.min(offset + pageSize, total) }} of {{ total }}</span>
            <button
              type="button"
              class="text-dim disabled:text-faint"
              :disabled="page === 0 || loading"
              @click="go(page - 1)"
            >
              Prev
            </button>
            <button
              type="button"
              class="text-dim disabled:text-faint"
              :disabled="page >= lastPage || loading"
              @click="go(page + 1)"
            >
              Next
            </button>
          </div>
        </div>
      </template>
    </ConsolePanel>
  </div>
</template>
