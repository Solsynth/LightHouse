<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'

import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AdminAccountSummary } from '@/lib/api/types'
import { punishmentTypeLabels } from '@/lib/enums'
import { relative, shortId } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'

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
      caught instanceof StargateError ? caught.message : 'Accounts could not be listed.'
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
    <PageHeader
      title="Accounts"
      description="Search the account table, open a record, or filter to restricted accounts."
    />

    <form class="mb-5 flex flex-wrap items-end gap-3" @submit.prevent="search">
      <div class="min-w-[220px] flex-1">
        <label for="account-query" class="field-label">Query</label>
        <input
          id="account-query"
          v-model="query"
          class="input input-sm"
          placeholder="name or nickname"
          autocapitalize="off"
        />
      </div>
      <label class="flex cursor-pointer items-center gap-2 pb-2 text-2xs text-dim">
        <input v-model="onlyPunished" class="checkbox checkbox-xs" type="checkbox" @change="search" />
        restricted only
      </label>
      <label class="flex cursor-pointer items-center gap-2 pb-2 text-2xs text-dim">
        <input v-model="onlyActivated" class="checkbox checkbox-xs" type="checkbox" @change="search" />
        activated only
      </label>
      <button type="submit" class="btn btn-primary btn-sm" :disabled="loading">Search</button>
    </form>

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <ConsolePanel title="Account Table" :note="`${total} matching`">
      <div v-if="loading" class="py-6 text-xs text-faint">Querying accounts…</div>

      <p v-else-if="!rows.length" class="voice py-6 text-[12px]">
        No accounts match. Clear the filters or try a different query.
      </p>

      <div v-else class="overflow-x-auto">
        <table class="ledger">
          <thead>
            <tr>
              <th scope="col">account</th>
              <th scope="col">email</th>
              <th scope="col">factors</th>
              <th scope="col">sessions</th>
              <th scope="col">state</th>
              <th scope="col">created</th>
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
                <span v-if="row.has_password" class="text-2xs text-faint">(pw)</span>
              </td>
              <td class="text-xs tabular-nums text-dim">
                {{ row.active_session_count }}
                <span class="text-2xs text-faint">/ {{ row.active_device_count }} dev</span>
              </td>
              <td>
                <span v-if="row.active_punishment" class="chip text-error">
                  {{ punishmentTypeLabels[row.active_punishment.type] ?? 'restricted' }}
                </span>
                <span v-else-if="row.account.activated_at" class="chip text-success">active</span>
                <span v-else class="chip text-warning">pending</span>
              </td>
              <td class="text-xs text-dim">{{ relative(row.account.created_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <template #footer>
        <div class="flex items-center justify-between text-2xs text-faint">
          <span>Page {{ page + 1 }} of {{ Math.max(1, Math.ceil(total / pageSize)) }}</span>
          <div class="flex items-center gap-3">
            <button
              type="button"
              class="text-dim disabled:text-faint"
              :disabled="page === 0 || loading"
              @click="go(page - 1)"
            >
              Prev
            </button>
            <button
              type="button"
              class="text-dim disabled:text-faint"
              :disabled="(page + 1) * pageSize >= total || loading"
              @click="go(page + 1)"
            >
              Next
            </button>
          </div>
        </div>
      </template>
    </ConsolePanel>
  </div>
</template>
