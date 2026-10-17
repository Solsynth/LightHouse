<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { PermissionGroup } from '@/lib/api/types'
import { relative } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'

const notice = useNoticeStore()

const groups = ref<PermissionGroup[]>([])
const total = ref(0)
const loading = ref(true)
const error = ref('')
const query = ref('')
const newKey = ref('')
const busy = ref(false)
const pendingDelete = ref<PermissionGroup | null>(null)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const result = await adminApi.groups({ query: query.value || undefined, take: 100 })
    groups.value = result.items
    total.value = result.total
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : 'Permission Groups could not be listed.'
  } finally {
    loading.value = false
  }
}

async function create() {
  if (!newKey.value.trim()) return
  busy.value = true
  error.value = ''
  try {
    await adminApi.createGroup(newKey.value.trim())
    notice.ok(`Created group ${newKey.value.trim()}.`)
    newKey.value = ''
    await load()
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : 'The group could not be created.'
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
    notice.ok(`Deleted group ${target.key}.`)
    pendingDelete.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : 'The group could not be deleted.')
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      title="Permission Groups"
      description="Groups hold permission keys and membership. Every route in the service checks the effective grant for the acting account."
    />

    <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_300px]">
      <div>
        <form class="mb-5 flex flex-wrap items-end gap-3" @submit.prevent="load">
          <div class="min-w-[200px] flex-1">
            <label for="group-query" class="field-label">Filter</label>
            <input
              id="group-query"
              v-model="query"
              class="input input-sm"
              placeholder="group key"
              autocapitalize="off"
            />
          </div>
          <button type="submit" class="btn btn-ghost btn-sm" :disabled="loading">Filter</button>
        </form>

        <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

        <ConsolePanel title="Groups" :note="`${total} total`">
          <div v-if="loading" class="py-6 text-xs text-faint">Reading groups…</div>

          <p v-else-if="!groups.length" class="voice py-6 text-[12px]">
            No groups match. The default group is seeded on first boot.
          </p>

          <div v-else class="overflow-x-auto">
            <table class="ledger">
              <thead>
                <tr>
                  <th scope="col">key</th>
                  <th scope="col">permissions</th>
                  <th scope="col">members</th>
                  <th scope="col">updated</th>
                  <th scope="col"><span class="sr-only">Actions</span></th>
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
                    <button
                      type="button"
                      class="text-2xs text-dim hover:text-error"
                      :disabled="busy"
                      @click="pendingDelete = group"
                    >
                      Delete
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </ConsolePanel>
      </div>

      <ConsolePanel title="New Group">
        <label for="new-group-key" class="field-label">Key</label>
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
          Create Group
        </button>
        <p class="voice mt-3 text-[12px] text-faint">
          Seeded groups: default, verified, moderator, developer. New Groups start empty.
        </p>
      </ConsolePanel>
    </div>

    <ConfirmAction
      v-if="pendingDelete"
      title="Delete This Group?"
      :body="`Every permission and membership in ${pendingDelete.key} is removed. Accounts holding it lose those grants on their next check.`"
      action="Delete Group"
      tone="error"
      :busy="busy"
      @cancel="pendingDelete = null"
      @confirm="remove"
    />
  </div>
</template>
