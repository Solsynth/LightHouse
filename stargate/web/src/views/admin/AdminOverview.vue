<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'

import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { CacheStats, GeographyBucket } from '@/lib/api/types'
import { bytes, compactNumber } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'

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
    error.value = caught instanceof StargateError ? caught.message : 'The overview could not be loaded.'
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
      title="Operations"
      description="Live counters for accounts, cache, and sign-in geography. Every figure is read from the service this console is talking to."
    >
      <template #actions>
        <button type="button" class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
          Refresh
        </button>
      </template>
    </PageHeader>

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <div class="grid gap-px border border-line bg-line sm:grid-cols-2 lg:grid-cols-4">
      <div class="bg-base-100 px-4 py-5">
        <p class="text-2xs uppercase tracking-[0.14em] text-faint">accounts</p>
        <p class="mt-2 text-2xl font-light tabular-nums text-base-content">
          {{ loading ? '—' : compactNumber(accounts) }}
        </p>
        <p class="mt-1 text-2xs text-faint">rows in the accounts table</p>
      </div>
      <div class="bg-base-100 px-4 py-5">
        <p class="text-2xs uppercase tracking-[0.14em] text-faint">under punishment</p>
        <p class="mt-2 text-2xl font-light tabular-nums text-base-content">
          {{ loading ? '—' : compactNumber(suspended) }}
        </p>
        <p class="mt-1 text-2xs text-faint">accounts with an active restriction</p>
      </div>
      <div class="bg-base-100 px-4 py-5">
        <p class="text-2xs uppercase tracking-[0.14em] text-faint">cache hit ratio</p>
        <p class="mt-2 text-2xl font-light tabular-nums text-base-content">
          {{ cacheAvailable ? `${(stats!.hit_ratio * 100).toFixed(1)}%` : 'offline' }}
        </p>
        <p class="mt-1 text-2xs text-faint">
          {{ cacheAvailable ? `${compactNumber(stats!.keyspace_hits)} hits` : 'redis not configured' }}
        </p>
      </div>
      <div class="bg-base-100 px-4 py-5">
        <p class="text-2xs uppercase tracking-[0.14em] text-faint">cache memory</p>
        <p class="mt-2 text-2xl font-light tabular-nums text-base-content">
          {{ cacheAvailable ? bytes(stats!.used_memory_bytes) : '—' }}
        </p>
        <p class="mt-1 text-2xs text-faint">
          {{ cacheAvailable ? `${stats!.connected_clients} clients` : 'no cache service' }}
        </p>
      </div>
    </div>

    <div class="mt-6 grid gap-6 lg:grid-cols-[minmax(0,1fr)_340px]">
      <ConsolePanel title="Sign-In Geography" note="last 30 days, by country">
        <p v-if="loading" class="py-6 text-xs text-faint">Reading session locations…</p>
        <p v-else-if="!topCountries.length" class="voice py-6 text-[12px]">
          No located sessions in this window. Locations appear once the geo database resolves a
          sign-in address.
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
            <span>{{ buckets.length }} countries reporting</span>
            <RouterLink to="/admin/geography" class="link-quiet">Open full map table</RouterLink>
          </div>
        </template>
      </ConsolePanel>

      <div class="space-y-6">
        <ConsolePanel title="Shortcuts">
          <ul class="space-y-2 text-xs">
            <li>
              <RouterLink to="/admin/accounts" class="link-quiet">Accounts</RouterLink>
              <span class="ml-2 text-faint">search, suspend, activate</span>
            </li>
            <li>
              <RouterLink to="/admin/permissions" class="link-quiet">Permission Groups</RouterLink>
              <span class="ml-2 text-faint">grants and membership</span>
            </li>
            <li>
              <RouterLink to="/admin/cache" class="link-quiet">Cache</RouterLink>
              <span class="ml-2 text-faint">stats, groups, invalidation</span>
            </li>
          </ul>
        </ConsolePanel>

        <ConsolePanel title="Service">
          <dl class="space-y-2 text-xs">
            <div class="flex justify-between">
              <dt class="text-faint">cache</dt>
              <dd :class="cacheAvailable ? 'text-success' : 'text-warning'">
                {{ cacheAvailable ? 'connected' : 'unavailable' }}
              </dd>
            </div>
            <div v-if="cacheAvailable" class="flex justify-between">
              <dt class="text-faint">commands</dt>
              <dd class="tabular-nums text-dim">{{ compactNumber(stats!.total_commands_processed) }}</dd>
            </div>
            <div v-if="cacheAvailable" class="flex justify-between">
              <dt class="text-faint">evicted</dt>
              <dd class="tabular-nums text-dim">{{ compactNumber(stats!.evicted_keys) }}</dd>
            </div>
            <div v-if="cacheAvailable" class="flex justify-between">
              <dt class="text-faint">expired</dt>
              <dd class="tabular-nums text-dim">{{ compactNumber(stats!.expired_keys) }}</dd>
            </div>
          </dl>
        </ConsolePanel>
      </div>
    </div>
  </div>
</template>
