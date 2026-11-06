<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Renew16 } from '@carbon/icons-vue'

import { activityApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { ActionLogSummary } from '@/lib/api/types'
import { relative } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import IconButton from '@/components/IconButton.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'

const { t, te } = useI18n()

const ranges = [7, 30, 90]
const range = ref(30)
const summary = ref<ActionLogSummary | null>(null)
const loading = ref(true)
const error = ref('')

const groups = computed(() => summary.value?.groups ?? [])
const windowTotal = computed(() => groups.value.reduce((total, group) => total + group.count, 0))
const maxGroupCount = computed(() => Math.max(1, ...groups.value.map((group) => group.count)))

// Zero-fill days so a quiet day still shows as a gap instead of collapsing
// the chart into a misleading run of adjacent bars.
const series = computed(() => {
  const data = summary.value
  if (!data) return []
  const counts = new Map<string, number>()
  for (const bucket of data.buckets) counts.set(bucket.start.slice(0, 10), bucket.count)
  const until = new Date(data.until)
  const end = Date.UTC(until.getUTCFullYear(), until.getUTCMonth(), until.getUTCDate())
  const points: { key: string; count: number }[] = []
  for (let offset = data.days - 1; offset >= 0; offset -= 1) {
    const key = new Date(end - offset * 86_400_000).toISOString().slice(0, 10)
    points.push({ key, count: counts.get(key) ?? 0 })
  }
  return points
})
const maxDayCount = computed(() => Math.max(1, ...series.value.map((point) => point.count)))

async function load() {
  loading.value = true
  error.value = ''
  try {
    summary.value = await activityApi.summary({ days: range.value })
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('activity.errorLoad')
  } finally {
    loading.value = false
  }
}

function setRange(days: number) {
  if (range.value === days) return
  range.value = days
  void load()
}

function share(count: number): string {
  return `${Math.max(1, Math.round((count / maxGroupCount.value) * 100))}%`
}

function percent(count: number): number {
  return windowTotal.value > 0 ? Math.round((count / windowTotal.value) * 100) : 0
}

function barHeight(count: number): string {
  return `${count > 0 ? Math.max(4, Math.round((count / maxDayCount.value) * 100)) : 1}%`
}

// Action ids are the stable contract; the localized label is only
// presentation. Unknown ids fall back to the raw id, so a new backend action
// is never hidden from the summary.
function actionLabel(action: string): string {
  const key = `activity.actions.${action.replace(/[^a-zA-Z0-9]+/g, '_')}`
  return te(key) ? t(key) : action
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader :title="t('activity.title')" :description="t('activity.description')">
      <template #actions>
        <IconButton :label="t('common.refresh')" :disabled="loading" @click="load">
          <Renew16 aria-hidden="true" />
        </IconButton>
      </template>
    </PageHeader>

    <p v-if="error" class="note note-error mb-4">{{ error }}</p>

    <div class="grid gap-2.5 sm:grid-cols-2 lg:grid-cols-4">
      <div class="rounded-card bg-base-200/50 px-4 py-5">
        <p class="text-2xs uppercase tracking-[0.14em] text-faint">
          {{ t('activity.last24h') }}
        </p>
        <p class="mt-2 text-2xl font-normal numeric text-base-content">
          {{ summary?.last_24h ?? 0 }}
        </p>
      </div>
      <div class="rounded-card bg-base-200/50 px-4 py-5">
        <p class="text-2xs uppercase tracking-[0.14em] text-faint">
          {{ t('activity.last7d') }}
        </p>
        <p class="mt-2 text-2xl font-normal numeric text-base-content">
          {{ summary?.last_7d ?? 0 }}
        </p>
      </div>
      <div class="rounded-card bg-base-200/50 px-4 py-5">
        <p class="text-2xs uppercase tracking-[0.14em] text-faint">
          {{ t('activity.last30d') }}
        </p>
        <p class="mt-2 text-2xl font-normal numeric text-base-content">
          {{ summary?.last_30d ?? 0 }}
        </p>
      </div>
      <div class="rounded-card bg-base-200/50 px-4 py-5">
        <p class="text-2xs uppercase tracking-[0.14em] text-faint">
          {{ t('activity.allTime') }}
        </p>
        <p class="mt-2 text-2xl font-normal numeric text-base-content">
          {{ summary?.total ?? 0 }}
        </p>
      </div>
    </div>

    <ConsolePanel
      class="mt-6"
      :title="t('activity.breakdown')"
      :note="t('activity.breakdownNote', { n: windowTotal })"
    >
      <div class="mb-4 flex flex-wrap items-center gap-2">
        <span class="text-2xs uppercase tracking-[0.12em] text-faint">
          {{ t('activity.window') }}
        </span>
        <button
          v-for="days in ranges"
          :key="days"
          type="button"
          class="chip transition-colors"
          :class="range === days ? 'chip-active' : ''"
          @click="setRange(days)"
        >
          {{ t('activity.days', { n: days }) }}
        </button>
      </div>

      <LoadingSkeleton v-if="loading" :rows="5" :columns="[4, 1]" header />

      <p v-else-if="!groups.length" class="voice py-6 text-[12px]">
        {{ t('activity.empty') }}
      </p>

      <ul v-else class="divide-y divide-line-soft">
        <li
          v-for="group in groups"
          :key="group.action"
          class="py-3 first:pt-0 last:pb-0"
        >
          <div class="flex items-baseline justify-between gap-3">
            <span class="text-xs text-base-content" :title="group.action">
              {{ actionLabel(group.action) }}
            </span>
            <span class="text-xs tabular-nums text-dim">{{ group.count }}</span>
          </div>
          <div class="mt-1.5 flex items-center gap-2">
            <div class="h-1.5 flex-1 overflow-hidden rounded-full bg-base-300/60">
              <div class="h-full rounded-full bg-primary" :style="{ width: share(group.count) }" />
            </div>
            <span class="w-9 text-right text-2xs tabular-nums text-faint">
              {{ percent(group.count) }}%
            </span>
          </div>
          <p class="mt-1 text-2xs text-faint">
            {{ t('activity.lastSeen', { when: relative(group.last_at) }) }}
          </p>
        </li>
      </ul>
    </ConsolePanel>

    <ConsolePanel
      class="mt-6"
      :title="t('activity.timeline')"
      :note="t('activity.timelineNote', { n: windowTotal })"
    >
      <div v-if="series.length" class="flex h-24 items-end gap-px sm:gap-0.5">
        <div
          v-for="point in series"
          :key="point.key"
          class="group flex h-full flex-1 items-end"
          :title="`${point.key} · ${point.count}`"
        >
          <div
            class="w-full rounded-t bg-primary/50 transition-colors group-hover:bg-primary"
            :style="{ height: barHeight(point.count) }"
          />
        </div>
      </div>
      <div v-if="series.length" class="mt-2 flex justify-between text-2xs text-faint">
        <span>{{ series[0]?.key }}</span>
        <span>{{ series[series.length - 1]?.key }}</span>
      </div>
      <p v-else class="voice py-6 text-[12px]">{{ t('activity.empty') }}</p>
    </ConsolePanel>
  </div>
</template>
