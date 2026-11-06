<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Renew16 } from '@carbon/icons-vue'
import { RouterLink } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { activityApi, adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { ActionLog, Punishment, WellbeingActivity, WellbeingStats } from '@/lib/api/types'
import { punishmentTypeLabel } from '@/lib/enums'
import { relative, shortId, utcStamp } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import AdminNav from '@/components/AdminNav.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import IconButton from '@/components/IconButton.vue'
import TextField from '@/components/TextField.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'

const { t } = useI18n()
const notice = useNoticeStore()

const stats = ref<WellbeingStats | null>(null)
const activity = ref<WellbeingActivity | null>(null)
const punishments = ref<Punishment[]>([])
const createdTotal = ref(0)
const logs = ref<ActionLog[]>([])
const logsTotal = ref(0)
const logAction = ref('')
const loading = ref(true)
const logsLoading = ref(false)
const error = ref('')
const busy = ref(false)
const pendingDelete = ref<Punishment | null>(null)

const activeShare = computed(() => {
  const value = activity.value
  if (!value || value.total_profiled_accounts === 0) return 0
  return Math.round((value.daily_active_users / value.total_profiled_accounts) * 100)
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [statsResult, activityResult, punishmentPage] = await Promise.all([
      adminApi.stats(),
      adminApi.activity(),
      adminApi.punishmentsCreated({ take: 25 }),
    ])
    stats.value = statsResult
    activity.value = activityResult
    punishments.value = punishmentPage.items
    createdTotal.value = punishmentPage.total
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('admin.audit.errorLoad')
  } finally {
    loading.value = false
  }
  await loadLogs()
}

async function loadLogs() {
  logsLoading.value = true
  try {
    const page = await activityApi.list({
      action: logAction.value.trim() || undefined,
      take: 25,
    })
    logs.value = page.items
    logsTotal.value = page.total
  } catch (caught) {
    notice.fail(caught instanceof StargateError ? caught.message : t('admin.audit.errorLogs'))
  } finally {
    logsLoading.value = false
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
        :note="t('admin.audit.logsNote', { n: logsTotal })"
      >
        <form class="mb-4 flex items-end gap-2" @submit.prevent="loadLogs">
          <TextField
            id="audit-action"
            v-model="logAction"
            size="xs"
            root-class="min-w-0 flex-1"
            :label="t('admin.audit.logFilter')"
            :placeholder="t('admin.audit.logFilterPlaceholder')"
          />
          <button type="submit" class="btn btn-ghost btn-xs shrink-0" :disabled="logsLoading">
            {{ t('common.search') }}
          </button>
        </form>

        <p v-if="!logs.length" class="voice py-6">{{ t('admin.audit.logsEmpty') }}</p>
        <ul v-else class="divide-y divide-line-soft">
          <li
            v-for="log in logs"
            :key="log.id"
            class="flex flex-wrap items-baseline justify-between gap-2 py-2.5 first:pt-0 last:pb-0"
          >
            <div class="min-w-0">
              <p class="font-mono text-2xs text-base-content">{{ log.action }}</p>
              <p class="mt-0.5 truncate text-2xs text-faint">
                {{ log.ip_address || t('admin.audit.noAddress') }}
                <template v-if="log.user_agent"> · {{ log.user_agent }}</template>
              </p>
            </div>
            <span class="text-2xs text-faint" :title="utcStamp(log.created_at)">
              {{ relative(log.created_at) }}
            </span>
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
