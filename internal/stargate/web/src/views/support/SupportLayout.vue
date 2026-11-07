<script setup lang="ts">
import { computed, onMounted, provide, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, RouterView, useRoute } from 'vue-router'

import { ticketApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { Ticket } from '@/lib/api/types'
import { relative } from '@/lib/format'
import { ticketStatusLabel } from '@/lib/enums'
import { TICKET_REFRESH, TICKET_STATUS, statusRail } from '@/lib/tickets'
import PageHeader from '@/components/PageHeader.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import TicketPriority from '@/components/tickets/TicketPriority.vue'

const { t } = useI18n()
const route = useRoute()

const pageSize = 50
const filter = ref<number | null>(null)
const tickets = ref<Ticket[]>([])
const loading = ref(true)
const loadingMore = ref(false)
const error = ref('')
const offset = ref(0)
const hasMore = ref(false)

const filters: { value: number | null; key: string }[] = [
  { value: null, key: 'all' },
  { value: TICKET_STATUS.open, key: 'open' },
  { value: TICKET_STATUS.inProgress, key: 'inProgress' },
  { value: TICKET_STATUS.waitingForCustomer, key: 'waitingCustomer' },
  { value: TICKET_STATUS.waitingForInformation, key: 'waitingInformation' },
  { value: TICKET_STATUS.resolved, key: 'resolved' },
  { value: TICKET_STATUS.closed, key: 'closed' },
]

const onIndex = computed(() => route.name === 'support')
const activeId = computed(() => (typeof route.params.id === 'string' ? route.params.id : ''))
const isFiltered = computed(() => filter.value !== null)

async function load(reset = true) {
  if (reset) {
    loading.value = true
    offset.value = 0
  } else {
    loadingMore.value = true
  }
  error.value = ''
  try {
    const batch = await ticketApi.mine({
      status: filter.value ?? undefined,
      take: pageSize,
      offset: reset ? 0 : offset.value,
    })
    tickets.value = reset ? batch : [...tickets.value, ...batch]
    offset.value = (reset ? 0 : offset.value) + batch.length
    hasMore.value = batch.length === pageSize
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('support.error.queue')
  } finally {
    loading.value = false
    loadingMore.value = false
  }
}

provide(TICKET_REFRESH, () => load(true))

watch(filter, () => {
  void load(true)
})

onMounted(() => {
  void load(true)
})
</script>

<template>
  <div>
    <PageHeader :title="t('support.title')" :description="t('support.description')">
      <template #actions>
        <RouterLink
          v-if="route.name !== 'support-new'"
          :to="{ name: 'support-new' }"
          class="btn btn-primary btn-sm"
        >
          {{ t('support.newRequest') }}
        </RouterLink>
      </template>
    </PageHeader>

    <div
      class="grid items-start gap-5 lg:grid-cols-[minmax(0,20rem)_minmax(0,1fr)] xl:grid-cols-[minmax(0,22rem)_minmax(0,1fr)]"
    >
      <section :class="onIndex ? '' : 'max-lg:hidden'" aria-labelledby="support-queue-title">
        <div class="flex items-baseline justify-between gap-3">
          <h2 id="support-queue-title" class="text-sm font-semibold text-base-content">
            {{ t('support.queue.title') }}
          </h2>
          <span class="text-2xs text-faint">
            {{ t('support.queue.note', { n: tickets.length }) }}
          </span>
        </div>

        <div
          class="mt-3 flex flex-wrap gap-1.5"
          role="group"
          :aria-label="t('support.queue.filterLabel')"
        >
          <button
            v-for="option in filters"
            :key="option.key"
            type="button"
            class="chip"
            :class="filter === option.value ? 'chip-active' : ''"
            :aria-pressed="filter === option.value"
            @click="filter = option.value"
          >
            {{ t(`support.queue.filters.${option.key}`) }}
          </button>
        </div>

        <p v-if="error" class="note note-error mt-3">{{ error }}</p>

        <div v-if="loading" class="mt-3">
          <LoadingSkeleton variant="rows" :rows="4" :rules="true" />
        </div>

        <div v-else-if="!tickets.length" class="mt-3 rounded-card bg-base-200/50 px-5 py-6">
          <p class="text-sm font-semibold text-base-content">
            {{ isFiltered ? t('support.queue.emptyFiltered') : t('support.queue.emptyTitle') }}
          </p>
          <p v-if="!isFiltered" class="voice mt-2 text-[13px]">
            {{ t('support.queue.emptyBody') }}
          </p>
          <RouterLink
            v-if="!isFiltered"
            :to="{ name: 'support-new' }"
            class="btn btn-primary btn-sm mt-4"
          >
            {{ t('support.newRequest') }}
          </RouterLink>
        </div>

        <div v-else class="mt-3 flex flex-col gap-1.5">
          <RouterLink
            v-for="ticket in tickets"
            :key="ticket.id"
            :to="{ name: 'support-ticket', params: { id: ticket.id } }"
            class="group relative block rounded-chip bg-raised py-3 pl-4 pr-3 transition-colors hover:bg-raised-hover"
            :class="activeId === ticket.id ? 'bg-raised-hover' : ''"
            :aria-current="activeId === ticket.id ? 'page' : undefined"
          >
            <span
              class="absolute bottom-2 left-0 top-2 w-[3px] rounded-full"
              :class="statusRail(ticket.status)"
              aria-hidden="true"
            />
            <div class="flex items-start justify-between gap-3">
              <p class="line-clamp-2 text-[13px] font-semibold leading-snug text-base-content">
                {{ ticket.title }}
              </p>
              <TicketPriority :priority="ticket.priority" class="mt-0.5 shrink-0" />
            </div>
            <div class="mt-2 flex items-center justify-between gap-2 text-2xs text-faint">
              <span class="truncate">{{ ticketStatusLabel(ticket.status) }}</span>
              <span class="shrink-0">{{ relative(ticket.updated_at) }}</span>
            </div>
          </RouterLink>

          <button
            v-if="hasMore"
            type="button"
            class="btn btn-ghost btn-sm mt-1"
            :disabled="loadingMore"
            @click="load(false)"
          >
            {{ loadingMore ? t('support.queue.loadingMore') : t('support.queue.loadMore') }}
          </button>
        </div>
      </section>

      <section :class="onIndex ? 'max-lg:hidden' : ''">
        <RouterView />
      </section>
    </div>
  </div>
</template>
