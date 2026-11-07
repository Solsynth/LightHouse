<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Renew16 } from '@carbon/icons-vue'
import { RouterLink } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { accountApi, adminApi, ticketApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { OnCallAdmin, Ticket } from '@/lib/api/types'
import { relative, shortId } from '@/lib/format'
import { ticketPriorityLabel, ticketStatusLabel, ticketTypeLabel } from '@/lib/enums'
import { TICKET_PRIORITIES, TICKET_STATUSES, TICKET_TYPES } from '@/lib/tickets'
import PageHeader from '@/components/PageHeader.vue'
import AdminNav from '@/components/AdminNav.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import IconButton from '@/components/IconButton.vue'
import TextField from '@/components/TextField.vue'
import TabBox from '@/components/TabBox.vue'
import TicketPriority from '@/components/tickets/TicketPriority.vue'
import TicketStatus from '@/components/tickets/TicketStatus.vue'
import type { Tab } from '@/components/tab'

const { t } = useI18n()
const notice = useNoticeStore()

const view = ref<'queue' | 'oncall'>('queue')

function selectView(key: string) {
  view.value = key === 'oncall' ? 'oncall' : 'queue'
}

const tabs = computed<Tab[]>(() => [
  { key: 'queue', label: t('admin.tickets.tabs.queue') },
  { key: 'oncall', label: t('admin.tickets.tabs.onCall') },
])

// ── Queue ──

const tickets = ref<Ticket[]>([])
const queueLoading = ref(true)
const queueLoadingMore = ref(false)
const queueError = ref('')
const statusFilter = ref(-1)
const typeFilter = ref(-1)
const priorityFilter = ref(-1)
const pageSize = 50
const offset = ref(0)
const hasMore = ref(false)

async function loadQueue(reset = true) {
  if (reset) {
    queueLoading.value = true
    offset.value = 0
  } else {
    queueLoadingMore.value = true
  }
  queueError.value = ''
  try {
    const batch = await ticketApi.list({
      status: statusFilter.value < 0 ? undefined : statusFilter.value,
      type: typeFilter.value < 0 ? undefined : typeFilter.value,
      priority: priorityFilter.value < 0 ? undefined : priorityFilter.value,
      take: pageSize,
      offset: reset ? 0 : offset.value,
    })
    tickets.value = reset ? batch : [...tickets.value, ...batch]
    offset.value = (reset ? 0 : offset.value) + batch.length
    hasMore.value = batch.length === pageSize
  } catch (caught) {
    queueError.value =
      caught instanceof StargateError ? caught.message : t('admin.tickets.errorQueue')
    if (reset) tickets.value = []
  } finally {
    queueLoading.value = false
    queueLoadingMore.value = false
  }
}

watch([statusFilter, typeFilter, priorityFilter], () => {
  void loadQueue(true)
})

// ── On-call roster ──

const roster = ref<OnCallAdmin[]>([])
const rosterLoading = ref(true)
const busy = ref(false)
const rosterError = ref('')
const accountName = ref('')

async function loadRoster() {
  rosterLoading.value = true
  rosterError.value = ''
  try {
    roster.value = await adminApi.onCall()
  } catch (caught) {
    rosterError.value =
      caught instanceof StargateError ? caught.message : t('admin.tickets.errorLoad')
  } finally {
    rosterLoading.value = false
  }
}

async function add() {
  const name = accountName.value.trim()
  if (!name) return
  busy.value = true
  try {
    const account = await accountApi.byName(name)
    await adminApi.addOnCall(account.id)
    notice.ok(t('admin.tickets.noticeAdded', { account: account.nick || account.name }))
    accountName.value = ''
    await loadRoster()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.tickets.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function remove(entry: OnCallAdmin) {
  busy.value = true
  try {
    await adminApi.removeOnCall(entry.account_id)
    notice.ok(t('admin.tickets.noticeRemoved'))
    await loadRoster()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.tickets.actionFailed'))
  } finally {
    busy.value = false
  }
}

function refresh() {
  if (view.value === 'queue') void loadQueue(true)
  else void loadRoster()
}

onMounted(() => {
  void loadQueue(true)
  void loadRoster()
})
</script>

<template>
  <div>
    <PageHeader :title="t('admin.tickets.title')" :description="t('admin.tickets.description')">
      <template #actions>
        <IconButton :label="t('common.refresh')" :disabled="busy" @click="refresh">
          <Renew16 aria-hidden="true" />
        </IconButton>
      </template>
    </PageHeader>

    <AdminNav />

    <TabBox
      :active="view"
      :tabs="tabs"
      :label="t('admin.tickets.tabsLabel')"
      @select="selectView"
    />

    <template v-if="view === 'queue'">
      <p v-if="queueError" class="note note-error mb-4">{{ queueError }}</p>

      <div class="mb-5 grid gap-3 sm:grid-cols-3">
        <TextField
          id="ticket-filter-status"
          v-model.number="statusFilter"
          as="select"
          :label="t('admin.tickets.filters.status')"
        >
          <option :value="-1">{{ t('admin.tickets.filters.all') }}</option>
          <option v-for="status in TICKET_STATUSES" :key="status" :value="status">
            {{ ticketStatusLabel(status) }}
          </option>
        </TextField>
        <TextField
          id="ticket-filter-type"
          v-model.number="typeFilter"
          as="select"
          :label="t('admin.tickets.filters.type')"
        >
          <option :value="-1">{{ t('admin.tickets.filters.all') }}</option>
          <option v-for="option in TICKET_TYPES" :key="option" :value="option">
            {{ ticketTypeLabel(option) }}
          </option>
        </TextField>
        <TextField
          id="ticket-filter-priority"
          v-model.number="priorityFilter"
          as="select"
          :label="t('admin.tickets.filters.priority')"
        >
          <option :value="-1">{{ t('admin.tickets.filters.all') }}</option>
          <option v-for="option in TICKET_PRIORITIES" :key="option" :value="option">
            {{ ticketPriorityLabel(option) }}
          </option>
        </TextField>
      </div>

      <ConsolePanel
        :title="t('admin.tickets.queueTitle')"
        :note="t('admin.tickets.queueNote', { n: tickets.length })"
      >
        <LoadingSkeleton v-if="queueLoading" :rows="5" :columns="[3, 2, 1, 2, 2, 1]" header />

        <p v-else-if="!tickets.length" class="voice py-6">
          {{ t('admin.tickets.emptyQueue') }}
        </p>

        <div v-else class="table-bleed overflow-x-auto">
          <table class="ledger">
            <thead>
              <tr>
                <th scope="col">{{ t('admin.tickets.columns.ticket') }}</th>
                <th scope="col">{{ t('admin.tickets.columns.type') }}</th>
                <th scope="col">{{ t('admin.tickets.columns.priority') }}</th>
                <th scope="col">{{ t('admin.tickets.columns.status') }}</th>
                <th scope="col">{{ t('admin.tickets.columns.assignee') }}</th>
                <th scope="col">{{ t('admin.tickets.columns.updated') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="ticket in tickets" :key="ticket.id">
                <td>
                  <RouterLink
                    :to="{ name: 'admin-ticket', params: { id: ticket.id } }"
                    class="block min-w-[14rem] max-w-[28rem] text-xs font-semibold text-base-content hover:underline"
                  >
                    {{ ticket.title }}
                  </RouterLink>
                  <span class="mt-0.5 block font-mono text-2xs text-faint">
                    #{{ shortId(ticket.id) }}
                  </span>
                </td>
                <td>
                  <span class="text-2xs text-dim">{{ ticketTypeLabel(ticket.type) }}</span>
                </td>
                <td>
                  <TicketPriority :priority="ticket.priority" />
                </td>
                <td>
                  <TicketStatus :status="ticket.status" quiet />
                </td>
                <td>
                  <span class="text-2xs text-dim">
                    {{
                      ticket.assignee
                        ? ticket.assignee.nick || ticket.assignee.name
                        : t('admin.tickets.unassigned')
                    }}
                  </span>
                </td>
                <td>
                  <span class="text-2xs text-faint">{{ relative(ticket.updated_at) }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <button
          v-if="hasMore"
          type="button"
          class="btn btn-ghost btn-sm mt-3"
          :disabled="queueLoadingMore"
          @click="loadQueue(false)"
        >
          {{ queueLoadingMore ? t('admin.tickets.loadingMore') : t('admin.tickets.loadMore') }}
        </button>
      </ConsolePanel>
    </template>

    <template v-else>
      <p v-if="rosterError" class="note note-error mb-4">{{ rosterError }}</p>

      <form class="mb-5 flex flex-wrap items-end gap-3" @submit.prevent="add">
        <TextField
          id="oncall-account"
          v-model="accountName"
          root-class="min-w-[220px] flex-1"
          :label="t('admin.tickets.accountName')"
          :hint="t('admin.tickets.accountHint')"
          placeholder="alice"
          autocapitalize="off"
        />
        <button
          type="submit"
          class="btn btn-primary btn-field"
          :disabled="busy || !accountName.trim()"
        >
          {{ t('admin.tickets.add') }}
        </button>
      </form>

      <ConsolePanel :title="t('admin.tickets.panelTitle')" :note="`${roster.length}`">
        <LoadingSkeleton v-if="rosterLoading" variant="rows" :rows="3" />

        <p v-else-if="!roster.length" class="voice py-6">{{ t('admin.tickets.empty') }}</p>

        <ul v-else class="divide-y divide-line-soft">
          <li
            v-for="entry in roster"
            :key="entry.id"
            class="flex flex-wrap items-center justify-between gap-2 py-2.5 first:pt-0 last:pb-0"
          >
            <div class="min-w-0">
              <RouterLink
                v-if="entry.account"
                :to="`/admin/accounts/${encodeURIComponent(entry.account.name)}`"
                class="text-xs text-base-content hover:underline"
              >
                {{ entry.account.nick || entry.account.name }}
              </RouterLink>
              <span v-else class="font-mono text-2xs text-faint">
                {{ shortId(entry.account_id) }}
              </span>
              <p class="mt-0.5 text-2xs text-faint">
                {{ t('admin.tickets.added', { time: relative(entry.created_at) }) }}
              </p>
            </div>
            <button
              type="button"
              class="btn btn-ghost btn-xs text-error"
              :disabled="busy"
              @click="remove(entry)"
            >
              {{ t('admin.tickets.remove') }}
            </button>
          </li>
        </ul>
      </ConsolePanel>
    </template>
  </div>
</template>
