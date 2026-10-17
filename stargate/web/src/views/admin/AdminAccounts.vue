<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronLeft16, ChevronRight16 } from '@carbon/icons-vue'
import { RouterLink } from 'vue-router'

import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AdminAccountSummary } from '@/lib/api/types'
import { punishmentTypeLabel } from '@/lib/enums'
import { relative, shortId } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'

const { t } = useI18n()

const rows = ref<AdminAccountSummary[]>([])
const total = ref(0)
const loading = ref(true)
const error = ref('')
const query = ref('')
const onlyPunished = ref(false)
const onlyActivated = ref(false)
const page = ref(0)
const pageSize = 50

async function load() {
  loading.value = true
  error.value = ''
  try {
    const result = await adminApi.accounts({
      query: query.value || undefined,
      hasPunishment: onlyPunished.value || undefined,
      activated: onlyActivated.value ? true : undefined,
      take: pageSize,
      offset: page.value * pageSize,
    })
    rows.value = result.items
    total.value = result.total
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('admin.accounts.errorLoad')
  } finally {
    loading.value = false
  }
}

function search() {
  page.value = 0
  void load()
}

function go(next: number) {
  page.value = Math.max(0, next)
  void load()
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader :title="t('admin.accounts.title')" :description="t('admin.accounts.description')" />

    <form class="mb-5 flex flex-wrap items-end gap-3" @submit.prevent="search">
      <div class="min-w-[220px] flex-1">
        <label for="account-query" class="field-label">{{ t('admin.accounts.query') }}</label>
        <input
          id="account-query"
          v-model="query"
          class="input input-sm"
          :placeholder="t('admin.accounts.queryPlaceholder')"
          autocapitalize="off"
        />
      </div>
      <label class="flex cursor-pointer items-center gap-2 pb-2 text-2xs text-dim">
        <input v-model="onlyPunished" class="checkbox checkbox-xs" type="checkbox" @change="search" />
        {{ t('admin.accounts.restrictedOnly') }}
      </label>
      <label class="flex cursor-pointer items-center gap-2 pb-2 text-2xs text-dim">
        <input v-model="onlyActivated" class="checkbox checkbox-xs" type="checkbox" @change="search" />
        {{ t('admin.accounts.activatedOnly') }}
      </label>
      <button type="submit" class="btn btn-primary btn-sm" :disabled="loading">
        {{ t('common.search') }}
      </button>
    </form>

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <ConsolePanel
      :title="t('admin.accounts.panelTitle')"
      :note="t('admin.accounts.matching', { n: total })"
    >
      <div v-if="loading" class="py-6 text-xs text-faint">{{ t('admin.accounts.loading') }}</div>

      <p v-else-if="!rows.length" class="voice py-6 text-[12px]">
        {{ t('admin.accounts.empty') }}
      </p>

      <div v-else class="overflow-x-auto">
        <table class="ledger">
          <thead>
            <tr>
              <th scope="col">{{ t('admin.accounts.colAccount') }}</th>
              <th scope="col">{{ t('admin.accounts.colEmail') }}</th>
              <th scope="col">{{ t('admin.accounts.colFactors') }}</th>
              <th scope="col">{{ t('admin.accounts.colSessions') }}</th>
              <th scope="col">{{ t('admin.accounts.colState') }}</th>
              <th scope="col">{{ t('common.created') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in rows" :key="row.account.id">
              <td>
                <RouterLink
                  :to="`/admin/accounts/${row.account.name}`"
                  class="text-xs text-base-content hover:underline"
                >
                  {{ row.account.name }}
                </RouterLink>
                <p class="text-2xs text-faint" :title="row.account.id">
                  {{ row.account.nick }} · {{ shortId(row.account.id) }}
                </p>
              </td>
              <td class="text-xs text-dim">{{ row.primary_email ?? '—' }}</td>
              <td class="text-xs tabular-nums text-dim">
                {{ row.auth_factor_count }}
                <span v-if="row.has_password" class="text-2xs text-faint">
                  {{ t('admin.accounts.passwordShort') }}
                </span>
              </td>
              <td class="text-xs tabular-nums text-dim">
                {{ row.active_session_count }}
                <span class="text-2xs text-faint">
                  / {{ t('admin.accounts.devices', { n: row.active_device_count }) }}
                </span>
              </td>
              <td>
                <span v-if="row.active_punishment" class="chip text-error">
                  {{ punishmentTypeLabel(row.active_punishment.type) }}
                </span>
                <span v-else-if="row.account.activated_at" class="chip text-success">
                  {{ t('common.active') }}
                </span>
                <span v-else class="chip text-warning">{{ t('common.pending') }}</span>
              </td>
              <td class="text-xs text-dim">{{ relative(row.account.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <template #footer>
        <div class="flex items-center justify-between text-2xs text-faint">
          <span>
            {{
              t('admin.accounts.page', {
                page: page + 1,
                pages: Math.max(1, Math.ceil(total / pageSize)),
              })
            }}
          </span>
          <div class="flex items-center gap-3">
            <button
              type="button"
              class="btn btn-ghost btn-xs px-1 text-dim disabled:text-faint"
              :aria-label="t('common.previousPage')"
              :title="t('common.previousPage')"
              :disabled="page === 0 || loading"
              @click="go(page - 1)"
            >
              <ChevronLeft16 aria-hidden="true" />
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-xs px-1 text-dim disabled:text-faint"
              :aria-label="t('common.nextPage')"
              :title="t('common.nextPage')"
              :disabled="(page + 1) * pageSize >= total || loading"
              @click="go(page + 1)"
            >
              <ChevronRight16 aria-hidden="true" />
            </button>
          </div>
        </div>
      </template>
    </ConsolePanel>
  </div>
</template>
