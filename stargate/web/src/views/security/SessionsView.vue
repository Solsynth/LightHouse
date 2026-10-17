<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import { useNoticeStore } from '@/stores/notice'
import { sessionApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AuthSession } from '@/lib/api/types'
import { describeAgent, place, relative, remaining, shortId, utcStamp } from '@/lib/format'
import { sessionTypeLabels } from '@/lib/enums'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import SecurityNav from '@/components/SecurityNav.vue'

const notice = useNoticeStore()

const sessions = ref<AuthSession[]>([])
const total = ref(0)
const loading = ref(true)
const error = ref('')
const pendingRevoke = ref<AuthSession | null>(null)
const busy = ref(false)

const active = computed(() => sessions.value.filter((item) => !item.is_current))

async function load() {
  loading.value = true
  error.value = ''
  try {
    const page = await sessionApi.list({ take: 100 })
    sessions.value = page.items
    total.value = page.total
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : 'Sessions could not be loaded.'
  } finally {
    loading.value = false
  }
}

async function revoke() {
  const target = pendingRevoke.value
  if (!target) return
  busy.value = true
  try {
    await sessionApi.revoke(target.id)
    notice.ok(`Revoked session ${shortId(target.id)}.`)
    pendingRevoke.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : 'The session could not be revoked.')
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      title="Sessions"
      description="Every credential currently able to act as this account. Revoking a session invalidates its tokens immediately."
    >
      <template #actions>
        <button type="button" class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
          Refresh
        </button>
      </template>
    </PageHeader>

    <SecurityNav />

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <ConsolePanel
      title="Active Sessions"
      :note="`${total} total · ${active.length} other than this browser`"
    >
      <div v-if="loading" class="py-6 text-xs text-faint">Reading session table…</div>

      <p v-else-if="!sessions.length" class="voice py-6">
        No sessions are recorded for this account.
      </p>

      <div v-else class="overflow-x-auto">
        <table class="ledger">
          <thead>
            <tr>
              <th scope="col">client</th>
              <th scope="col">origin</th>
              <th scope="col">type</th>
              <th scope="col">granted</th>
              <th scope="col">expires</th>
              <th scope="col"><span class="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in sessions" :key="item.id">
              <td>
                <div class="flex items-center gap-2">
                  <span
                    class="dot"
                    :class="item.is_online ? 'bg-success' : 'bg-line'"
                    :title="item.is_online ? 'Connected now' : 'Offline'"
                  />
                  <span class="text-xs text-base-content">{{ describeAgent(item.user_agent) }}</span>
                  <span v-if="item.is_current" class="chip text-primary">this browser</span>
                  <span v-if="item.trusted" class="chip text-accent">trusted</span>
                </div>
                <p class="mt-1 text-2xs text-faint" :title="item.id">{{ shortId(item.id) }}</p>
              </td>
              <td>
                <p class="text-xs text-dim">{{ item.ip_address || '—' }}</p>
                <p class="text-2xs text-faint">{{ place(item.location) }}</p>
              </td>
              <td class="text-xs text-dim">
                {{ sessionTypeLabels[item.type] ?? 'session' }}
                <p v-if="item.scopes.length" class="text-2xs text-faint">
                  {{ item.scopes.slice(0, 3).join(' ') }}
                </p>
              </td>
              <td class="text-xs text-dim" :title="utcStamp(item.last_granted_at)">
                {{ relative(item.last_granted_at) }}
              </td>
              <td class="text-xs text-dim">{{ remaining(item.expired_at) }}</td>
              <td>
                <button
                  v-if="!item.is_current"
                  type="button"
                  class="text-2xs text-dim hover:text-error"
                  @click="pendingRevoke = item"
                >
                  Revoke
                </button>
                <span v-else class="text-2xs text-faint">—</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <template #footer>
        <p class="voice text-[12px] text-faint">
          This browser's session is the one issuing these requests. Revoking it signs you out
          everywhere this cookie was accepted.
        </p>
      </template>
    </ConsolePanel>

    <ConfirmAction
      v-if="pendingRevoke"
      title="Revoke This Session?"
      :body="`The client ${describeAgent(pendingRevoke.user_agent)} loses its tokens immediately. It must sign in again.`"
      action="Revoke Session"
      tone="error"
      :busy="busy"
      @cancel="pendingRevoke = null"
      @confirm="revoke"
    />
  </div>
</template>
