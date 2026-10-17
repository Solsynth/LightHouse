<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { GeographyBucket } from '@/lib/api/types'
import { utcStamp } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'

const { t } = useI18n()

const buckets = ref<GeographyBucket[]>([])
const precision = ref<'country' | 'city'>('country')
const loading = ref(true)
const error = ref('')

const totalUsers = computed(() => buckets.value.reduce((sum, bucket) => sum + bucket.user_count, 0))
const largest = computed(() =>
  buckets.value.reduce((max, bucket) => Math.max(max, bucket.user_count), 1),
)

async function load() {
  loading.value = true
  error.value = ''
  try {
    buckets.value = await adminApi.geography(precision.value)
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('admin.geography.errorLoad')
  } finally {
    loading.value = false
  }
}

function setPrecision(value: 'country' | 'city') {
  precision.value = value
  void load()
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      :title="t('admin.geography.title')"
      :description="t('admin.geography.description')"
    >
      <template #actions>
        <div class="flex gap-1 border border-line p-0.5">
          <button
            type="button"
            class="px-2 py-1 text-2xs"
            :class="precision === 'country' ? 'bg-base-300 text-base-content' : 'text-faint'"
            @click="setPrecision('country')"
          >
            {{ t('admin.geography.country') }}
          </button>
          <button
            type="button"
            class="px-2 py-1 text-2xs"
            :class="precision === 'city' ? 'bg-base-300 text-base-content' : 'text-faint'"
            @click="setPrecision('city')"
          >
            {{ t('admin.geography.city') }}
          </button>
        </div>
      </template>
    </PageHeader>

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <ConsolePanel
      :title="precision === 'country' ? t('admin.geography.byCountry') : t('admin.geography.byCity')"
      :note="t('admin.geography.panelNote', { buckets: buckets.length, total: totalUsers })"
    >
      <div v-if="loading" class="py-6 text-xs text-faint">
        {{ t('admin.geography.loading') }}
      </div>

      <p v-else-if="!buckets.length" class="voice py-6 text-[12px]">
        {{ t('admin.geography.empty') }}
      </p>

      <div v-else class="overflow-x-auto">
        <table class="ledger">
          <thead>
            <tr>
              <th scope="col">{{ t('admin.geography.colPlace') }}</th>
              <th scope="col">{{ t('admin.geography.colAccounts') }}</th>
              <th scope="col" class="w-[40%]">{{ t('admin.geography.colShare') }}</th>
              <th scope="col">{{ t('admin.geography.colCentroid') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="bucket in buckets" :key="`${bucket.country_code}:${bucket.city ?? ''}`">
              <td class="text-xs text-base-content">
                {{ bucket.city ? `${bucket.city}, ${bucket.country ?? bucket.country_code}` : bucket.country ?? bucket.country_code }}
                <span class="ml-1 text-2xs text-faint">{{ bucket.country_code }}</span>
              </td>
              <td class="text-xs tabular-nums text-dim">{{ bucket.user_count }}</td>
              <td>
                <div class="flex items-center gap-2">
                  <div class="h-[3px] flex-1 bg-base-300">
                    <div
                      class="h-[3px] bg-primary/70"
                      :style="{ width: `${(bucket.user_count / largest) * 100}%` }"
                    />
                  </div>
                  <span class="w-10 text-right text-2xs tabular-nums text-faint">
                    {{ ((bucket.user_count / totalUsers) * 100).toFixed(1) }}%
                  </span>
                </div>
              </td>
              <td class="text-2xs tabular-nums text-faint">
                {{ bucket.latitude.toFixed(1) }}, {{ bucket.longitude.toFixed(1) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <template #footer>
        <p class="text-2xs text-faint">
          {{ t('admin.geography.footer', { stamp: utcStamp(new Date().toISOString()) }) }}
        </p>
      </template>
    </ConsolePanel>
  </div>
</template>
