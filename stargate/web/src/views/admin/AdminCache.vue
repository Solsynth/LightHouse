<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { useNoticeStore } from '@/stores/notice'
import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { CacheGroup, CacheStats } from '@/lib/api/types'
import { bytes, compactNumber } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'

const { t } = useI18n()
const notice = useNoticeStore()

const stats = ref<CacheStats | null>(null)
const loading = ref(true)
const error = ref('')
const busy = ref(false)

const groupName = ref('auth:session')
const group = ref<CacheGroup | null>(null)
const groupError = ref('')
const keyToClear = ref('')
const pendingClear = ref<'all' | 'group' | null>(null)

const ratio = computed(() => {
  if (!stats.value) return 0
  return Math.round(stats.value.hit_ratio * 1000) / 10
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    stats.value = await adminApi.cacheStats()
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('admin.cache.errorStats')
    stats.value = null
  } finally {
    loading.value = false
  }
}

async function inspect() {
  if (!groupName.value.trim()) return
  busy.value = true
  groupError.value = ''
  try {
    group.value = await adminApi.cacheGroup(groupName.value.trim())
  } catch (caught) {
    group.value = null
    groupError.value = caught instanceof Error ? caught.message : t('admin.cache.errorGroup')
  } finally {
    busy.value = false
  }
}

async function clearKey() {
  if (!keyToClear.value.trim()) return
  busy.value = true
  try {
    const result = await adminApi.clearCacheKey(keyToClear.value.trim())
    notice.ok(t('admin.cache.noticeKeyRemoved', { n: result.removed_count }))
    keyToClear.value = ''
    await inspect()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.cache.errorKey'))
  } finally {
    busy.value = false
  }
}

async function clearConfirmed() {
  busy.value = true
  try {
    if (pendingClear.value === 'all') {
      const result = await adminApi.clearCache()
      notice.ok(t('admin.cache.noticeAllCleared', { n: result.removed_count }))
    } else if (pendingClear.value === 'group' && group.value) {
      const result = await adminApi.clearCacheGroup(group.value.group)
      notice.ok(
        t('admin.cache.noticeGroupCleared', {
          n: result.removed_count,
          group: group.value.group,
        }),
      )
    }
    pendingClear.value = null
    group.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.cache.errorCache'))
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader :title="t('admin.cache.title')" :description="t('admin.cache.description')">
      <template #actions>
        <button type="button" class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
          {{ t('common.refresh') }}
        </button>
      </template>
    </PageHeader>

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <div v-if="stats" class="grid gap-px border border-line bg-line sm:grid-cols-2 lg:grid-cols-4">
      <div class="bg-base-100 px-4 py-4">
        <p class="text-2xs uppercase tracking-[0.14em] text-faint">
          {{ t('admin.cache.statHitRatio') }}
        </p>
        <p class="mt-2 text-xl font-light tabular-nums text-base-content">{{ ratio }}%</p>
      </div>
      <div class="bg-base-100 px-4 py-4">
        <p class="text-2xs uppercase tracking-[0.14em] text-faint">
          {{ t('admin.cache.statMemory') }}
        </p>
        <p class="mt-2 text-xl font-light tabular-nums text-base-content">
          {{ bytes(stats.used_memory_bytes) }}
        </p>
      </div>
      <div class="bg-base-100 px-4 py-4">
        <p class="text-2xs uppercase tracking-[0.14em] text-faint">
          {{ t('admin.cache.statClients') }}
        </p>
        <p class="mt-2 text-xl font-light tabular-nums text-base-content">
          {{ stats.connected_clients }}
        </p>
      </div>
      <div class="bg-base-100 px-4 py-4">
        <p class="text-2xs uppercase tracking-[0.14em] text-faint">
          {{ t('admin.cache.statCommands') }}
        </p>
        <p class="mt-2 text-xl font-light tabular-nums text-base-content">
          {{ compactNumber(stats.total_commands_processed) }}
        </p>
      </div>
    </div>

    <div v-if="stats" class="mt-6 grid gap-6 lg:grid-cols-2">
      <ConsolePanel :title="t('admin.cache.counters')">
        <dl class="space-y-3 text-xs">
          <div class="flex justify-between border-b border-line-soft pb-2">
            <dt class="text-faint">{{ t('admin.cache.keyspaceHits') }}</dt>
            <dd class="tabular-nums text-dim">{{ compactNumber(stats.keyspace_hits) }}</dd>
          </div>
          <div class="flex justify-between border-b border-line-soft pb-2">
            <dt class="text-faint">{{ t('admin.cache.keyspaceMisses') }}</dt>
            <dd class="tabular-nums text-dim">{{ compactNumber(stats.keyspace_misses) }}</dd>
          </div>
          <div class="flex justify-between border-b border-line-soft pb-2">
            <dt class="text-faint">{{ t('admin.cache.evictedKeys') }}</dt>
            <dd class="tabular-nums text-dim">{{ compactNumber(stats.evicted_keys) }}</dd>
          </div>
          <div class="flex justify-between border-b border-line-soft pb-2">
            <dt class="text-faint">{{ t('admin.cache.expiredKeys') }}</dt>
            <dd class="tabular-nums text-dim">{{ compactNumber(stats.expired_keys) }}</dd>
          </div>
          <div class="flex justify-between">
            <dt class="text-faint">{{ t('admin.cache.reads') }}</dt>
            <dd class="tabular-nums text-dim">{{ compactNumber(stats.read_count) }}</dd>
          </div>
        </dl>
      </ConsolePanel>

      <ConsolePanel :title="t('admin.cache.invalidation')">
        <label for="cache-key" class="field-label">{{ t('admin.cache.clearOneKey') }}</label>
        <div class="flex gap-2">
          <input
            id="cache-key"
            v-model="keyToClear"
            class="input input-sm flex-1"
            placeholder="auth:session:…"
            autocapitalize="off"
            spellcheck="false"
            @keydown.enter="clearKey"
          />
          <button
            type="button"
            class="btn btn-ghost btn-sm"
            :disabled="busy || !keyToClear.trim()"
            @click="clearKey"
          >
            {{ t('admin.cache.clear') }}
          </button>
        </div>

        <button
          type="button"
          class="btn btn-error btn-sm mt-5 w-full"
          :disabled="busy"
          @click="pendingClear = 'all'"
        >
          {{ t('admin.cache.clearAll') }}
        </button>
        <p class="voice mt-2 text-[12px] text-faint">
          {{ t('admin.cache.invalidationHint') }}
        </p>
      </ConsolePanel>
    </div>

    <ConsolePanel
      :title="t('admin.cache.inspectTitle')"
      class="mt-6"
      :note="t('admin.cache.inspectNote')"
    >
      <div class="flex flex-wrap gap-2">
        <input
          v-model="groupName"
          class="input input-sm min-w-[200px] flex-1"
          placeholder="auth:session"
          autocapitalize="off"
          spellcheck="false"
          @keydown.enter="inspect"
        />
        <button type="button" class="btn btn-ghost btn-sm" :disabled="busy" @click="inspect">
          {{ t('admin.cache.inspect') }}
        </button>
        <button
          v-if="group && group.count > 0"
          type="button"
          class="btn btn-error btn-sm"
          :disabled="busy"
          @click="pendingClear = 'group'"
        >
          {{ t('admin.cache.clearGroup') }}
        </button>
      </div>

      <p v-if="groupError" class="mt-3 text-xs text-error">{{ groupError }}</p>

      <div v-if="group" class="mt-4">
        <p class="text-2xs text-faint">
          {{ t('admin.cache.groupCount', { n: group.count, group: group.group }) }}
        </p>
        <ul v-if="group.keys.length" class="mt-2 max-h-64 divide-y divide-line-soft overflow-y-auto">
          <li v-for="key in group.keys" :key="key" class="break-all py-1.5 text-2xs text-dim">
            {{ key }}
          </li>
        </ul>
        <p v-else class="voice mt-2 text-[12px]">{{ t('admin.cache.noKeys') }}</p>
      </div>
    </ConsolePanel>

    <ConfirmAction
      v-if="pendingClear"
      :title="
        pendingClear === 'all'
          ? t('admin.cache.confirmClearAll')
          : t('admin.cache.confirmClearGroup')
      "
      :body="
        pendingClear === 'all'
          ? t('admin.cache.confirmClearBodyAll')
          : t('admin.cache.confirmClearBodyGroup', {
              n: group?.count ?? 0,
              group: group?.group,
            })
      "
      :action="t('admin.cache.confirmClearAction')"
      tone="error"
      :busy="busy"
      @cancel="pendingClear = null"
      @confirm="clearConfirmed"
    />
  </div>
</template>
