<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Checkmark16 } from '@carbon/icons-vue'

import { useNoticeStore } from '@/stores/notice'
import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { WalletOrder, WalletProductSummary } from '@/lib/api/types'
import { relative, utcStamp } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import AdminNav from '@/components/AdminNav.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import TextField from '@/components/TextField.vue'

const { t } = useI18n()
const notice = useNoticeStore()

const product = ref<WalletProductSummary | null>(null)
const orderId = ref('')
const applied = ref<WalletOrder | null>(null)
const loading = ref(true)
const busy = ref(false)
const error = ref('')

const providers = ref<Array<{ name: string; plans: Array<{ id: string; price: number }> }>>([])

async function load() {
  loading.value = true
  error.value = ''
  try {
    product.value = await adminApi.walletProduct()
    providers.value = Object.entries(product.value.provider_mappings).map(([name, plans]) => ({
      name,
      plans: Object.entries(plans).map(([id, price]) => ({ id, price })),
    }))
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('admin.wallet.errorLoad')
  } finally {
    loading.value = false
  }
}

async function applyOrder() {
  const id = orderId.value.trim()
  if (!id) return
  busy.value = true
  try {
    applied.value = await adminApi.applyWalletOrder(id)
    notice.ok(t('admin.wallet.noticeApplied', { id }))
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.wallet.actionFailed'))
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader :title="t('admin.wallet.title')" :description="t('admin.wallet.description')" />
    <AdminNav />

    <p v-if="error" class="note note-error mb-4">{{ error }}</p>

    <LoadingSkeleton v-if="loading" :rows="4" :columns="[3, 2, 2]" header />

    <div v-else class="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_340px]">
      <div class="space-y-6">
        <ConsolePanel
          v-if="product"
          :title="t('admin.wallet.productTitle')"
          :note="product.key"
        >
          <dl class="grid gap-x-6 gap-y-3 sm:grid-cols-2">
            <div class="flex justify-between pb-2 text-xs">
              <dt class="text-faint">{{ t('admin.wallet.identifier') }}</dt>
              <dd class="font-mono text-dim">{{ product.identifier }}</dd>
            </div>
            <div class="flex justify-between pb-2 text-xs">
              <dt class="text-faint">{{ t('admin.wallet.displayName') }}</dt>
              <dd class="text-dim">{{ product.display_name }}</dd>
            </div>
            <div class="flex justify-between pb-2 text-xs">
              <dt class="text-faint">{{ t('admin.wallet.currency') }}</dt>
              <dd class="text-dim">{{ product.currency }}</dd>
            </div>
          </dl>

          <div v-if="providers.length" class="mt-4 space-y-3">
            <div v-for="provider in providers" :key="provider.name">
              <p class="text-2xs uppercase tracking-[0.12em] text-faint">{{ provider.name }}</p>
              <ul class="mt-1 flex flex-wrap gap-2">
                <li v-for="plan in provider.plans" :key="plan.id" class="chip">
                  <span class="font-mono">{{ plan.id }}</span>
                  <span class="text-faint">{{ plan.price }}</span>
                </li>
              </ul>
            </div>
          </div>
        </ConsolePanel>

        <ConsolePanel :title="t('admin.wallet.applyTitle')">
          <form class="flex flex-wrap items-end gap-3" @submit.prevent="applyOrder">
            <TextField
              id="wallet-order"
              v-model="orderId"
              root-class="min-w-[260px] flex-1"
              :label="t('admin.wallet.orderId')"
              :hint="t('admin.wallet.orderHint')"
              placeholder="00000000-0000-0000-0000-000000000000"
              control-class="font-mono"
              autocapitalize="off"
            />
            <button
              type="submit"
              class="btn btn-primary btn-field"
              :disabled="busy || !orderId.trim()"
            >
              <Checkmark16 aria-hidden="true" />
              {{ t('admin.wallet.apply') }}
            </button>
          </form>

          <div v-if="applied" class="mt-5 rounded-card bg-base-200/40 p-4">
            <dl class="grid gap-x-6 gap-y-3 sm:grid-cols-2">
              <div class="flex justify-between text-xs">
                <dt class="text-faint">{{ t('admin.wallet.orderId') }}</dt>
                <dd class="break-all font-mono text-dim">{{ applied.id }}</dd>
              </div>
              <div class="flex justify-between text-xs">
                <dt class="text-faint">{{ t('admin.wallet.status') }}</dt>
                <dd class="tabular-nums text-dim">{{ applied.status }}</dd>
              </div>
              <div class="flex justify-between text-xs">
                <dt class="text-faint">{{ t('admin.wallet.amount') }}</dt>
                <dd class="tabular-nums text-dim">{{ applied.amount }} {{ applied.currency }}</dd>
              </div>
              <div class="flex justify-between text-xs">
                <dt class="text-faint">{{ t('admin.wallet.updated') }}</dt>
                <dd class="text-dim" :title="utcStamp(applied.updated_at)">
                  {{ relative(applied.updated_at) }}
                </dd>
              </div>
            </dl>
          </div>
        </ConsolePanel>
      </div>

      <ConsolePanel :title="t('admin.wallet.guideTitle')">
        <ol class="ml-4 list-decimal space-y-2 text-[12px] text-dim">
          <li>{{ t('admin.wallet.guideLookup') }}</li>
          <li>{{ t('admin.wallet.guideApply') }}</li>
          <li>{{ t('admin.wallet.guideVerify') }}</li>
        </ol>
      </ConsolePanel>
    </div>
  </div>
</template>
