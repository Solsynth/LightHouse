<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Renew16 } from '@carbon/icons-vue'
import { RouterLink } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { accountApi, adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { OnCallAdmin } from '@/lib/api/types'
import { relative, shortId } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import AdminNav from '@/components/AdminNav.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import IconButton from '@/components/IconButton.vue'
import TextField from '@/components/TextField.vue'

const { t } = useI18n()
const notice = useNoticeStore()

const roster = ref<OnCallAdmin[]>([])
const loading = ref(true)
const busy = ref(false)
const error = ref('')
const accountName = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    roster.value = await adminApi.onCall()
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('admin.tickets.errorLoad')
  } finally {
    loading.value = false
  }
}

async function add() {
  const name = accountName.value.trim()
  if (!name) return
  busy.value = true
  try {
    const account = await accountApi.byName(name)
    await adminApi.addOnCall(account.id)
    notice.ok(t('admin.tickets.noticeAdded', { account: account.nick || account.name }))
    accountName.value = ''
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.tickets.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function remove(entry: OnCallAdmin) {
  busy.value = true
  try {
    await adminApi.removeOnCall(entry.account_id)
    notice.ok(t('admin.tickets.noticeRemoved'))
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.tickets.actionFailed'))
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader :title="t('admin.tickets.title')" :description="t('admin.tickets.description')">
      <template #actions>
        <IconButton :label="t('common.refresh')" :disabled="loading" @click="load">
          <Renew16 aria-hidden="true" />
        </IconButton>
      </template>
    </PageHeader>

    <AdminNav />

    <p v-if="error" class="note note-error mb-4">{{ error }}</p>

    <form class="mb-5 flex flex-wrap items-end gap-3" @submit.prevent="add">
      <TextField
        id="oncall-account"
        v-model="accountName"
        root-class="min-w-[220px] flex-1"
        :label="t('admin.tickets.accountName')"
        :hint="t('admin.tickets.accountHint')"
        placeholder="alice"
        autocapitalize="off"
      />
      <button type="submit" class="btn btn-primary btn-field" :disabled="busy || !accountName.trim()">
        {{ t('admin.tickets.add') }}
      </button>
    </form>

    <ConsolePanel :title="t('admin.tickets.panelTitle')" :note="`${roster.length}`">
      <LoadingSkeleton v-if="loading" variant="rows" :rows="3" />

      <p v-else-if="!roster.length" class="voice py-6">{{ t('admin.tickets.empty') }}</p>

      <ul v-else class="divide-y divide-line-soft">
        <li
          v-for="entry in roster"
          :key="entry.id"
          class="flex flex-wrap items-center justify-between gap-2 py-2.5 first:pt-0 last:pb-0"
        >
          <div class="min-w-0">
            <RouterLink
              v-if="entry.account"
              :to="`/admin/accounts/${encodeURIComponent(entry.account.name)}`"
              class="text-xs text-base-content hover:underline"
            >
              {{ entry.account.nick || entry.account.name }}
            </RouterLink>
            <span v-else class="font-mono text-2xs text-faint">
              {{ shortId(entry.account_id) }}
            </span>
            <p class="mt-0.5 text-2xs text-faint">
              {{ t('admin.tickets.added', { time: relative(entry.created_at) }) }}
            </p>
          </div>
          <button
            type="button"
            class="btn btn-ghost btn-xs text-error"
            :disabled="busy"
            @click="remove(entry)"
          >
            {{ t('admin.tickets.remove') }}
          </button>
        </li>
      </ul>
    </ConsolePanel>
  </div>
</template>
