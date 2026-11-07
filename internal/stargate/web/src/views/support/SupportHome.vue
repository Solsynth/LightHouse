<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'

import { ticketStatusLabel } from '@/lib/enums'
import { TICKET_STATUS, statusRail } from '@/lib/tickets'

const { t } = useI18n()

const quick = [
  { type: 1, key: 'bug' },
  { type: 3, key: 'billing' },
  { type: 2, key: 'feature' },
]

const statusKeys: Record<number, string> = {
  [TICKET_STATUS.open]: 'open',
  [TICKET_STATUS.inProgress]: 'inProgress',
  [TICKET_STATUS.waitingForCustomer]: 'waitingCustomer',
  [TICKET_STATUS.waitingForInformation]: 'waitingInformation',
  [TICKET_STATUS.resolved]: 'resolved',
  [TICKET_STATUS.closed]: 'closed',
}

const statuses = [
  TICKET_STATUS.open,
  TICKET_STATUS.inProgress,
  TICKET_STATUS.waitingForCustomer,
  TICKET_STATUS.waitingForInformation,
  TICKET_STATUS.resolved,
  TICKET_STATUS.closed,
]
</script>

<template>
  <div class="rounded-card bg-base-200/50 px-6 py-7">
    <h2 class="title max-w-md text-xl text-base-content">
      {{ t('support.home.heading') }}
    </h2>
    <p class="voice mt-3 max-w-xl text-[13.5px]">
      {{ t('support.home.body') }}
    </p>

    <h3 class="mt-7 text-sm font-semibold text-base-content">
      {{ t('support.home.startHeading') }}
    </h3>
    <ul class="mt-2 divide-y divide-line-soft">
      <li v-for="entry in quick" :key="entry.key">
        <RouterLink
          :to="{ name: 'support-new', query: { type: entry.type } }"
          class="block rounded-chip px-2 py-3 transition-colors hover:bg-base-300/50"
        >
          <span class="text-[13px] font-semibold text-base-content">
            {{ t(`support.home.quick.${entry.key}`) }}
          </span>
          <span class="mt-0.5 block text-2xs text-faint">
            {{ t(`support.home.quick.${entry.key}Detail`) }}
          </span>
        </RouterLink>
      </li>
    </ul>

    <h3 class="mt-7 text-sm font-semibold text-base-content">
      {{ t('support.home.statusHeading') }}
    </h3>
    <dl class="mt-2 space-y-2.5">
      <div v-for="status in statuses" :key="status" class="flex gap-3">
        <span class="mt-[7px] dot" :class="statusRail(status)" aria-hidden="true" />
        <div class="min-w-0">
          <dt class="text-2xs font-semibold text-dim">{{ ticketStatusLabel(status) }}</dt>
          <dd class="mt-0.5 text-2xs leading-relaxed text-faint">
            {{ t(`support.home.status.${statusKeys[status]}`) }}
          </dd>
        </div>
      </div>
    </dl>
  </div>
</template>
