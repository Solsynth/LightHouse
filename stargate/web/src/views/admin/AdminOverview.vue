<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Renew16 } from '@carbon/icons-vue'
import { RouterLink } from 'vue-router'

import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { CacheStats, GeographyBucket } from '@/lib/api/types'
import { bytes, compactNumber } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import IconButton from '@/components/IconButton.vue'

const { t } = useI18n()

const stats = ref<CacheStats | null>(null)
const buckets = ref<GeographyBucket[]>([])
const accounts = ref(0)
const suspended = ref(0)
const loading = ref(true)
const error = ref('')

const topCountries = computed(() => {
  const totals = new Map<string, number>()
  for (const bucket of buckets.value) {
    const key = bucket.country || bucket.country_code
    totals.set(key, (totals.get(key) ?? 0) + bucket.user_count)
  }
  return [...totals.entries()].sort((a, b) => b[1] - a[1]).slice(0, 6)
})

const largest = computed(() => topCountries.value[0]?.[1] ?? 1)

const cacheAvailable = computed(() => stats.value !== null)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [accountPage, punished, geography] = await Promise.all([
      adminApi.accounts({ take: 1 }),
      adminApi.accounts({ take: 1, hasPunishment: true }),
      adminApi.geography('country').catch(() => [] as GeographyBucket[]),
    ])
    accounts.value = accountPage.total
    suspended.value = punished.total
    buckets.value = geography
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('admin.overview.errorLoad')
  } finally {
    try {
      stats.value = await adminApi.cacheStats()
    } catch {
      stats.value = null
    }
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      :title="t('admin.overview.title')"
      :description="t('admin.overview.description')"
    >
      <template #actions>
        <IconButton :label="t('common.refresh')" :disabled="loading" @click="load">
          <Renew16 aria-hidden="true" />
        </IconButton>
      </template>
    </PageHeader>

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <div class="grid gap-px border border-line bg-line sm:grid-cols-2 lg:grid-cols-4">
      <div class="bg-base-100 px-4 py-5">
        <p class="text-2xs uppercase tracking-[0.14em] text-faint">
          {{ t('admin.overview.statAccounts') }}
        </p>
        <p class="mt-2 text-2xl font-light tabular-nums text-base-content">
          <span v-if="loading" class="skeleton h-6 w-16" aria-hidden="true" />
          <template v-else>{{ compactNumber(accounts) }}</template>
        </p>
        <p class="mt-1 text-2xs text-faint">{{ t('admin.overview.statAccountsNote') }}</p>
      </div>
      <div class="bg-base-100 px-4 py-5">
        <p class="text-2xs uppercase tracking-[0.14em] text-faint">
          {{ t('admin.overview.statRestricted') }}
        </p>
        <p class="mt-2 text-2xl font-light tabular-nums text-base-content">
          <span v-if="loading" class="skeleton h-6 w-16" aria-hidden="true" />
          <template v-else>{{ compactNumber(suspended) }}</template>
        </p>
        <p class="mt-1 text-2xs text-faint">{{ t('admin.overview.statRestrictedNote') }}</p>
      </div>
      <div class="bg-base-100 px-4 py-5">
        <p class="text-2xs uppercase tracking-[0.14em] text-faint">
          {{ t('admin.overview.statHitRatio') }}
        </p>
        <p class="mt-2 text-2xl font-light tabular-nums text-base-content">
          {{ cacheAvailable ? `${(stats!.hit_ratio * 100).toFixed(1)}%` : t('admin.overview.statOffline') }}
        </p>
        <p class="mt-1 text-2xs text-faint">
          {{
            cacheAvailable
              ? t('admin.overview.statHits', { n: compactNumber(stats!.keyspace_hits) })
              : t('admin.overview.noRedis')
          }}
        </p>
      </div>
      <div class="bg-base-100 px-4 py-5">
        <p class="text-2xs uppercase tracking-[0.14em] text-faint">
          {{ t('admin.overview.statMemory') }}
        </p>
        <p class="mt-2 text-2xl font-light tabular-nums text-base-content">
          {{ cacheAvailable ? bytes(stats!.used_memory_bytes) : '—' }}
        </p>
        <p class="mt-1 text-2xs text-faint">
          {{
            cacheAvailable
              ? t('admin.overview.statClients', { n: stats!.connected_clients })
              : t('admin.overview.noCacheService')
          }}
        </p>
      </div>
    </div>

    <div class="mt-6 grid gap-6 lg:grid-cols-[minmax(0,1fr)_340px]">
      <ConsolePanel
        :title="t('admin.overview.geographyTitle')"
        :note="t('admin.overview.geographyNote')"
      >
        <LoadingSkeleton v-if="loading" :rows="4" :columns="[4, 2, 2]" header />
        <p v-else-if="!topCountries.length" class="voice py-6 text-[12px]">
          {{ t('admin.overview.geographyEmpty') }}
        </p>

        <ul v-else class="space-y-3">
          <li v-for="[country, count] in topCountries" :key="country">
            <div class="flex items-baseline justify-between text-xs">
              <span class="text-dim">{{ country }}</span>
              <span class="tabular-nums text-faint">{{ count }}</span>
            </div>
            <div class="mt-1.5 h-[3px] w-full bg-base-300">
              <div class="h-[3px] bg-primary/70" :style="{ width: `${(count / largest) * 100}%` }" />
            </div>
          </li>
        </ul>

        <template #footer>
          <div class="flex flex-wrap items-center justify-between gap-2 text-2xs text-faint">
            <span>{{ t('admin.overview.countriesReporting', { n: buckets.length }) }}</span>
            <RouterLink to="/admin/geography" class="link-quiet">
              {{ t('admin.overview.openMap') }}
            </RouterLink>
          </div>
        </template>
      </ConsolePanel>

      <div class="space-y-6">
        <ConsolePanel :title="t('admin.overview.shortcuts')">
          <ul class="space-y-2 text-xs">
            <li>
              <RouterLink to="/admin/accounts" class="link-quiet">
                {{ t('admin.overview.shortcutAccounts') }}
              </RouterLink>
              <span class="ml-2 text-faint">{{ t('admin.overview.shortcutAccountsHint') }}</span>
            </li>
            <li>
              <RouterLink to="/admin/permissions" class="link-quiet">
                {{ t('admin.overview.shortcutGroups') }}
              </RouterLink>
              <span class="ml-2 text-faint">{{ t('admin.overview.shortcutGroupsHint') }}</span>
            </li>
            <li>
              <RouterLink to="/admin/cache" class="link-quiet">
                {{ t('admin.overview.shortcutCache') }}
              </RouterLink>
              <span class="ml-2 text-faint">{{ t('admin.overview.shortcutCacheHint') }}</span>
            </li>
          </ul>
        </ConsolePanel>

        <ConsolePanel :title="t('admin.overview.service')">
          <dl class="space-y-2 text-xs">
            <div class="flex justify-between">
              <dt class="text-faint">{{ t('admin.overview.shortcutCache') }}</dt>
              <dd :class="cacheAvailable ? 'text-success' : 'text-warning'">
                {{ cacheAvailable ? t('admin.overview.connected') : t('admin.overview.unavailable') }}
              </dd>
            </div>
            <div v-if="cacheAvailable" class="flex justify-between">
              <dt class="text-faint">{{ t('admin.overview.commands') }}</dt>
              <dd class="tabular-nums text-dim">{{ compactNumber(stats!.total_commands_processed) }}</dd>
            </div>
            <div v-if="cacheAvailable" class="flex justify-between">
              <dt class="text-faint">{{ t('admin.overview.evicted') }}</dt>
              <dd class="tabular-nums text-dim">{{ compactNumber(stats!.evicted_keys) }}</dd>
            </div>
            <div v-if="cacheAvailable" class="flex justify-between">
              <dt class="text-faint">{{ t('admin.overview.expired') }}</dt>
              <dd class="tabular-nums text-dim">{{ compactNumber(stats!.expired_keys) }}</dd>
            </div>
          </dl>
        </ConsolePanel>
      </div>
    </div>
  </div>
</template>
