<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronLeft16, ChevronRight16, Close16, Renew16 } from '@carbon/icons-vue'

import { useNoticeStore } from '@/stores/notice'
import { sessionApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AuthSession } from '@/lib/api/types'
import { describeAgent, place, relative, remaining, shortId, utcStamp } from '@/lib/format'
import { sessionTypeLabel } from '@/lib/enums'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import IconButton from '@/components/IconButton.vue'
import SecurityNav from '@/components/SecurityNav.vue'

const { t } = useI18n()
const notice = useNoticeStore()

const sessions = ref<AuthSession[]>([])
const total = ref(0)
const loading = ref(true)
const error = ref('')
const pendingRevoke = ref<AuthSession | null>(null)
const busy = ref(false)
const page = ref(0)
const pageSize = 25

async function load() {
  loading.value = true
  error.value = ''
  try {
    const result = await sessionApi.list({ take: pageSize, offset: page.value * pageSize })
    sessions.value = result.items
    total.value = result.total
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('sessions.errorLoad')
  } finally {
    loading.value = false
  }
}

function go(next: number) {
  page.value = Math.max(0, Math.min(next, Math.max(0, Math.ceil(total.value / pageSize) - 1)))
  void load()
}

async function revoke() {
  const target = pendingRevoke.value
  if (!target) return
  busy.value = true
  try {
    await sessionApi.revoke(target.id)
    notice.ok(t('sessions.noticeRevoked', { id: shortId(target.id) }))
    pendingRevoke.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('sessions.errorRevoke'))
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      :title="t('sessions.title')"
      :description="t('sessions.description')"
    >
      <template #actions>
        <IconButton :label="t('common.refresh')" :disabled="loading" @click="load">
          <Renew16 aria-hidden="true" />
        </IconButton>
      </template>
    </PageHeader>

    <SecurityNav />

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <ConsolePanel
      :title="t('sessions.activeSessions')"
      :note="t('sessions.note', { total })"
    >
      <LoadingSkeleton v-if="loading" :rows="4" :columns="[3, 2, 2, 2, 1]" header />

      <p v-else-if="!sessions.length" class="voice py-6">
        {{ t('sessions.empty') }}
      </p>

      <div v-else class="table-bleed overflow-x-auto">
        <table class="ledger">
          <thead>
            <tr>
              <th scope="col">{{ t('sessions.client') }}</th>
              <th scope="col">{{ t('sessions.origin') }}</th>
              <th scope="col">{{ t('sessions.type') }}</th>
              <th scope="col">{{ t('sessions.granted') }}</th>
              <th scope="col">{{ t('common.expires') }}</th>
              <th scope="col"><span class="sr-only">{{ t('common.actions') }}</span></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="item in sessions" :key="item.id">
              <td>
                <div class="flex items-center gap-2">
                  <span
                    class="dot"
                    :class="item.is_online ? 'bg-success' : 'bg-line'"
                    :title="item.is_online ? t('sessions.connectedNow') : t('common.offline')"
                  />
                  <span class="text-xs text-base-content">{{ describeAgent(item.user_agent) }}</span>
                  <span v-if="item.is_current" class="chip text-primary">{{ t('sessions.thisBrowser') }}</span>
                  <span v-if="item.trusted" class="chip text-accent">{{ t('sessions.trusted') }}</span>
                </div>
                <p class="mt-1 text-2xs text-faint" :title="item.id">{{ shortId(item.id) }}</p>
              </td>
              <td>
                <p class="text-xs text-dim">{{ item.ip_address || '—' }}</p>
                <p class="text-2xs text-faint">{{ place(item.location) }}</p>
              </td>
              <td class="text-xs text-dim">
                {{ sessionTypeLabel(item.type) || t('sessions.fallbackType') }}
                <p v-if="item.scopes.length" class="text-2xs text-faint">
                  {{ item.scopes.slice(0, 3).join(' ') }}
                </p>
              </td>
              <td class="text-xs text-dim" :title="utcStamp(item.last_granted_at)">
                {{ relative(item.last_granted_at) }}
              </td>
              <td class="text-xs text-dim">{{ remaining(item.expired_at) }}</td>
              <td>
                <IconButton
                  v-if="!item.is_current"
                  :label="t('sessions.revoke')"
                  tone="danger"
                  @click="pendingRevoke = item"
                >
                  <Close16 aria-hidden="true" />
                </IconButton>
                <span v-else class="text-2xs text-faint">—</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <template #footer>
        <div class="flex flex-wrap items-center justify-between gap-3 text-2xs text-faint">
          <span>{{ t('sessions.footer') }}</span>
          <div class="flex items-center gap-3">
            <span v-if="!loading" class="tabular-nums">
              {{
                t('sessions.page', {
                  page: page + 1,
                  pages: Math.max(1, Math.ceil(total / pageSize)),
                })
              }}
            </span>
            <button
              type="button"
              class="btn btn-ghost btn-xs px-1 text-dim disabled:text-faint"
              :aria-label="t('common.previousPage')"
              :title="t('common.previousPage')"
              :disabled="page === 0 || loading"
              @click="go(page - 1)"
            >
              <ChevronLeft16 aria-hidden="true" />
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-xs px-1 text-dim disabled:text-faint"
              :aria-label="t('common.nextPage')"
              :title="t('common.nextPage')"
              :disabled="page >= Math.ceil(total / pageSize) - 1 || loading"
              @click="go(page + 1)"
            >
              <ChevronRight16 aria-hidden="true" />
            </button>
          </div>
        </div>
      </template>
    </ConsolePanel>

    <ConfirmAction
      v-if="pendingRevoke"
      :title="t('sessions.revokeTitle')"
      :body="t('sessions.revokeBody', { client: describeAgent(pendingRevoke.user_agent) })"
      :action="t('sessions.revokeAction')"
      tone="error"
      :busy="busy"
      @cancel="pendingRevoke = null"
      @confirm="revoke"
    />
  </div>
</template>
