<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronLeft16, ChevronRight16, TrashCan16 } from '@carbon/icons-vue'
import { RouterLink } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { PermissionGroup } from '@/lib/api/types'
import { relative } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import IconButton from '@/components/IconButton.vue'

const { t } = useI18n()
const notice = useNoticeStore()

const groups = ref<PermissionGroup[]>([])
const total = ref(0)
const loading = ref(true)
const error = ref('')
const query = ref('')
const newKey = ref('')
const busy = ref(false)
const pendingDelete = ref<PermissionGroup | null>(null)
const page = ref(0)
const pageSize = 25

async function load() {
  loading.value = true
  error.value = ''
  try {
    const result = await adminApi.groups({
      query: query.value || undefined,
      take: pageSize,
      offset: page.value * pageSize,
    })
    groups.value = result.items
    total.value = result.total
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('admin.permissions.errorLoad')
  } finally {
    loading.value = false
  }
}

function search() {
  page.value = 0
  void load()
}

function go(next: number) {
  page.value = Math.max(0, Math.min(next, Math.max(0, Math.ceil(total.value / pageSize) - 1)))
  void load()
}

async function create() {
  if (!newKey.value.trim()) return
  busy.value = true
  error.value = ''
  try {
    await adminApi.createGroup(newKey.value.trim())
    notice.ok(t('admin.permissions.created', { key: newKey.value.trim() }))
    newKey.value = ''
    await load()
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('admin.permissions.errorCreate')
  } finally {
    busy.value = false
  }
}

async function remove() {
  const target = pendingDelete.value
  if (!target) return
  busy.value = true
  try {
    await adminApi.deleteGroup(target.id)
    notice.ok(t('admin.permissions.deleted', { key: target.key }))
    pendingDelete.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.permissions.errorDelete'))
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      :title="t('admin.permissions.title')"
      :description="t('admin.permissions.description')"
    />

    <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_300px]">
      <div>
        <form class="mb-5 flex flex-wrap items-end gap-3" @submit.prevent="search">
          <div class="min-w-[200px] flex-1">
            <label for="group-query" class="field-label">{{ t('admin.permissions.filter') }}</label>
            <input
              id="group-query"
              v-model="query"
              class="input input-sm"
              :placeholder="t('admin.permissions.filterPlaceholder')"
              autocapitalize="off"
            />
          </div>
          <button type="submit" class="btn btn-ghost btn-sm" :disabled="loading">
            {{ t('admin.permissions.filter') }}
          </button>
        </form>

        <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

        <ConsolePanel
          :title="t('admin.permissions.panelTitle')"
          :note="t('admin.permissions.panelNote', { n: total })"
        >
          <div v-if="loading" class="py-6 text-xs text-faint">
            {{ t('admin.permissions.loading') }}
          </div>

          <p v-else-if="!groups.length" class="voice py-6 text-[12px]">
            {{ t('admin.permissions.empty') }}
          </p>

          <div v-else class="overflow-x-auto">
            <table class="ledger">
              <thead>
                <tr>
                  <th scope="col">{{ t('admin.permissions.colKey') }}</th>
                  <th scope="col">{{ t('admin.permissions.colPermissions') }}</th>
                  <th scope="col">{{ t('admin.permissions.colMembers') }}</th>
                  <th scope="col">{{ t('common.updated') }}</th>
                  <th scope="col"><span class="sr-only">{{ t('common.actions') }}</span></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="group in groups" :key="group.id">
                  <td>
                    <RouterLink
                      :to="`/admin/permissions/${group.id}`"
                      class="text-xs text-base-content hover:underline"
                    >
                      {{ group.key }}
                    </RouterLink>
                  </td>
                  <td class="text-xs tabular-nums text-dim">{{ group.node_count }}</td>
                  <td class="text-xs tabular-nums text-dim">{{ group.member_count }}</td>
                  <td class="text-xs text-dim">{{ relative(group.updated_at) }}</td>
                  <td>
                    <IconButton
                      :label="t('common.delete')"
                      tone="danger"
                      :disabled="busy"
                      @click="pendingDelete = group"
                    >
                      <TrashCan16 aria-hidden="true" />
                    </IconButton>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <template #footer>
            <div class="flex items-center justify-between text-2xs text-faint">
              <span class="tabular-nums">
                {{
                  t('admin.permissions.page', {
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
                  :disabled="page >= Math.ceil(total / pageSize) - 1 || loading"
                  @click="go(page + 1)"
                >
                  <ChevronRight16 aria-hidden="true" />
                </button>
              </div>
            </div>
          </template>
        </ConsolePanel>
      </div>

      <ConsolePanel :title="t('admin.permissions.newGroup')">
        <label for="new-group-key" class="field-label">{{ t('admin.permissions.key') }}</label>
        <input
          id="new-group-key"
          v-model="newKey"
          class="input input-sm"
          placeholder="moderator"
          autocapitalize="off"
          @keydown.enter="create"
        />
        <button
          type="button"
          class="btn btn-primary btn-sm mt-4 w-full"
          :disabled="busy || !newKey.trim()"
          @click="create"
        >
          {{ t('admin.permissions.createGroup') }}
        </button>
        <p class="voice mt-3 text-[12px] text-faint">
          {{ t('admin.permissions.seededHint') }}
        </p>
      </ConsolePanel>
    </div>

    <ConfirmAction
      v-if="pendingDelete"
      :title="t('admin.permissions.confirmDeleteTitle')"
      :body="t('admin.permissions.confirmDeleteBody', { key: pendingDelete.key })"
      :action="t('admin.permissions.confirmDeleteAction')"
      tone="error"
      :busy="busy"
      @cancel="pendingDelete = null"
      @confirm="remove"
    />
  </div>
</template>
