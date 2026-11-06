<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Renew16 } from '@carbon/icons-vue'
import { RouterLink } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { activityApi, adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { ActionLogSummary, Punishment, WellbeingActivity, WellbeingStats } from '@/lib/api/types'
import { punishmentTypeLabel } from '@/lib/enums'
import { relative, shortId, utcStamp } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import AdminNav from '@/components/AdminNav.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import IconButton from '@/components/IconButton.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'

const { t } = useI18n()
const notice = useNoticeStore()

const stats = ref<WellbeingStats | null>(null)
const activity = ref<WellbeingActivity | null>(null)
const punishments = ref<Punishment[]>([])
const createdTotal = ref(0)
const summary = ref<ActionLogSummary | null>(null)
const loading = ref(true)
const error = ref('')
const busy = ref(false)
const pendingDelete = ref<Punishment | null>(null)

const activeShare = computed(() => {
  const value = activity.value
  if (!value || value.total_profiled_accounts === 0) return 0
  return Math.round((value.daily_active_users / value.total_profiled_accounts) * 100)
})

const groups = computed(() => summary.value?.groups ?? [])
const windowTotal = computed(() => groups.value.reduce((total, group) => total + group.count, 0))
const maxGroupCount = computed(() => Math.max(1, ...groups.value.map((group) => group.count)))

function share(count: number): string {
  return `${Math.max(1, Math.round((count / maxGroupCount.value) * 100))}%`
}

function percent(count: number): number {
  return windowTotal.value > 0 ? Math.round((count / windowTotal.value) * 100) : 0
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [statsResult, activityResult, punishmentPage, summaryResult] = await Promise.all([
      adminApi.stats(),
      adminApi.activity(),
      adminApi.punishmentsCreated({ take: 25 }),
      activityApi.summary({ days: 30 }),
    ])
    stats.value = statsResult
    activity.value = activityResult
    punishments.value = punishmentPage.items
    createdTotal.value = punishmentPage.total
    summary.value = summaryResult
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('admin.audit.errorLoad')
  } finally {
    loading.value = false
  }
}

async function removePunishment() {
  const target = pendingDelete.value
  if (!target) return
  busy.value = true
  try {
    await adminApi.deletePunishment(target.account?.name ?? '', target.id)
    notice.ok(t('admin.audit.noticeLifted', { id: shortId(target.id) }))
    pendingDelete.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof StargateError ? caught.message : t('admin.audit.actionFailed'))
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader :title="t('admin.audit.title')" :description="t('admin.audit.description')">
      <template #actions>
        <IconButton :label="t('common.refresh')" :disabled="loading" @click="load">
          <Renew16 aria-hidden="true" />
        </IconButton>
      </template>
    </PageHeader>

    <AdminNav />

    <p v-if="error" class="note note-error mb-4">{{ error }}</p>

    <LoadingSkeleton v-if="loading" :rows="4" :columns="[3, 2, 2, 2]" header />

    <template v-else>
      <div class="grid gap-2.5 sm:grid-cols-2 lg:grid-cols-4">
        <div class="rounded-card bg-base-200/50 px-4 py-5">
          <p class="text-2xs uppercase tracking-[0.14em] text-faint">
            {{ t('admin.audit.statProfiled') }}
          </p>
          <p class="mt-2 text-2xl font-normal numeric text-base-content">
            {{ stats?.total_profiled_accounts ?? 0 }}
          </p>
          <p class="mt-1 text-2xs text-faint">{{ t('admin.audit.statProfiledNote') }}</p>
        </div>
        <div class="rounded-card bg-base-200/50 px-4 py-5">
          <p class="text-2xs uppercase tracking-[0.14em] text-faint">
            {{ t('admin.audit.statDaily') }}
          </p>
          <p class="mt-2 text-2xl font-normal numeric text-base-content">
            {{ activity?.daily_active_users ?? 0 }}
          </p>
          <p class="mt-1 text-2xs text-faint">
            {{ t('admin.audit.statShare', { percent: activeShare }) }}
          </p>
        </div>
        <div class="rounded-card bg-base-200/50 px-4 py-5">
          <p class="text-2xs uppercase tracking-[0.14em] text-faint">
            {{ t('admin.audit.statNewToday') }}
          </p>
          <p class="mt-2 text-2xl font-normal numeric text-base-content">
            {{ activity?.new_accounts_today ?? 0 }}
          </p>
          <p class="mt-1 text-2xs text-faint">
            {{ t('admin.audit.statNewWeek', { n: activity?.new_accounts_this_week ?? 0 }) }}
          </p>
        </div>
        <div class="rounded-card bg-base-200/50 px-4 py-5">
          <p class="text-2xs uppercase tracking-[0.14em] text-faint">
            {{ t('admin.audit.statWeekly') }}
          </p>
          <p class="mt-2 text-2xl font-normal numeric text-base-content">
            {{ activity?.weekly_active_users ?? 0 }}
          </p>
          <p class="mt-1 text-2xs text-faint">
            {{ t('admin.audit.statMonthly', { n: activity?.monthly_active_users ?? 0 }) }}
          </p>
        </div>
      </div>

      <ConsolePanel
        class="mt-6"
        :title="t('admin.audit.punishmentsTitle')"
        :note="t('admin.audit.punishmentsNote', { n: createdTotal })"
      >
        <p v-if="!punishments.length" class="voice py-6">
          {{ t('admin.audit.punishmentsEmpty') }}
        </p>
        <div v-else class="table-bleed overflow-x-auto">
          <table class="ledger">
            <thead>
              <tr>
                <th scope="col">{{ t('admin.audit.colAccount') }}</th>
                <th scope="col">{{ t('admin.audit.colType') }}</th>
                <th scope="col">{{ t('admin.audit.colReason') }}</th>
                <th scope="col">{{ t('admin.audit.colCreated') }}</th>
                <th scope="col">{{ t('common.expires') }}</th>
                <th scope="col"><span class="sr-only">{{ t('common.actions') }}</span></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in punishments" :key="item.id">
                <td>
                  <RouterLink
                    v-if="item.account"
                    :to="`/admin/accounts/${encodeURIComponent(item.account.name)}`"
                    class="text-xs text-base-content hover:underline"
                  >
                    {{ item.account.nick || item.account.name }}
                  </RouterLink>
                  <span v-else class="text-xs text-faint">{{ shortId(item.account_id) }}</span>
                </td>
                <td class="text-xs text-dim">{{ punishmentTypeLabel(item.type) }}</td>
                <td class="max-w-[320px] text-xs text-dim">
                  <p class="line-clamp-2">{{ item.reason }}</p>
                </td>
                <td class="text-xs text-dim" :title="utcStamp(item.created_at)">
                  {{ relative(item.created_at) }}
                </td>
                <td class="text-xs text-dim">
                  {{ item.expired_at ? utcStamp(item.expired_at).slice(0, 10) : '—' }}
                </td>
                <td>
                  <button
                    type="button"
                    class="btn btn-ghost btn-xs text-error"
                    :disabled="busy || !item.account"
                    @click="pendingDelete = item"
                  >
                    {{ t('admin.audit.lift') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </ConsolePanel>

      <ConsolePanel
        class="mt-6"
        :title="t('admin.audit.logsTitle')"
        :note="t('admin.audit.logsNote', { n: summary?.last_30d ?? 0 })"
      >
        <p class="mb-4 text-2xs text-faint">
          {{
            t('admin.audit.logsWindow', {
              day: summary?.last_24h ?? 0,
              week: summary?.last_7d ?? 0,
            })
          }}
        </p>

        <p v-if="!groups.length" class="voice py-6">{{ t('admin.audit.logsEmpty') }}</p>
        <ul v-else class="divide-y divide-line-soft">
          <li
            v-for="group in groups"
            :key="group.action"
            class="py-2.5 first:pt-0 last:pb-0"
          >
            <div class="flex items-baseline justify-between gap-3">
              <p class="min-w-0 truncate font-mono text-2xs text-base-content" :title="group.action">
                {{ group.action }}
              </p>
              <span class="text-xs tabular-nums text-dim">{{ group.count }}</span>
            </div>
            <div class="mt-1 flex items-center gap-2">
              <div class="h-1 flex-1 overflow-hidden rounded-full bg-base-300/60">
                <div class="h-full rounded-full bg-primary" :style="{ width: share(group.count) }" />
              </div>
              <span class="w-9 text-right text-2xs tabular-nums text-faint">
                {{ percent(group.count) }}%
              </span>
            </div>
            <p class="mt-0.5 text-2xs text-faint">
              {{ t('admin.audit.lastSeen', { when: relative(group.last_at) }) }}
            </p>
          </li>
        </ul>
      </ConsolePanel>
    </template>

    <ConfirmAction
      v-if="pendingDelete"
      :title="t('admin.audit.confirmLiftTitle')"
      :body="t('admin.audit.confirmLiftBody', { account: pendingDelete.account?.nick || pendingDelete.account?.name || '' })"
      :action="t('admin.audit.lift')"
      tone="error"
      :busy="busy"
      @cancel="pendingDelete = null"
      @confirm="removePunishment"
    />
  </div>
</template>
