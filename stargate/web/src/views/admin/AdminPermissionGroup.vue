<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { PermissionGroupDetail } from '@/lib/api/types'
import { actorTypeLabels } from '@/lib/enums'
import { relative, remaining } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'

const route = useRoute()
const router = useRouter()
const notice = useNoticeStore()

const detail = ref<PermissionGroupDetail | null>(null)
const loading = ref(true)
const error = ref('')
const busy = ref(false)

const permissionKey = ref('')
const memberActor = ref('')
const pendingNode = ref<string | null>(null)
const pendingMember = ref<string | null>(null)

const id = computed(() => String(route.params.id ?? ''))
const sortedNodes = computed(() =>
  [...(detail.value?.nodes ?? [])].sort((a, b) => a.key.localeCompare(b.key)),
)

async function load() {
  loading.value = true
  error.value = ''
  try {
    detail.value = await adminApi.group(id.value)
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : 'The group could not be read.'
  } finally {
    loading.value = false
  }
}

async function grant() {
  if (!permissionKey.value.trim()) return
  busy.value = true
  error.value = ''
  try {
    await adminApi.grantPermission(id.value, permissionKey.value.trim())
    notice.ok(`Granted ${permissionKey.value.trim()}.`)
    permissionKey.value = ''
    await load()
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : 'The grant failed.'
  } finally {
    busy.value = false
  }
}

async function revoke() {
  const key = pendingNode.value
  if (!key) return
  busy.value = true
  try {
    await adminApi.revokePermission(id.value, key)
    notice.ok(`Revoked ${key}.`)
    pendingNode.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : 'The revoke failed.')
  } finally {
    busy.value = false
  }
}

async function addMember() {
  if (!memberActor.value.trim()) return
  busy.value = true
  error.value = ''
  try {
    await adminApi.addMember(id.value, memberActor.value.trim())
    notice.ok('Member added.')
    memberActor.value = ''
    await load()
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : 'The member could not be added.'
  } finally {
    busy.value = false
  }
}

async function removeMember() {
  const actor = pendingMember.value
  if (!actor) return
  busy.value = true
  try {
    await adminApi.removeMember(id.value, actor)
    notice.ok('Member removed.')
    pendingMember.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : 'The member could not be removed.')
  } finally {
    busy.value = false
  }
}

watch(() => route.params.id, load)
onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      :title="detail?.group.key ?? 'Permission Group'"
      :description="`${detail?.node_total ?? 0} permissions · ${detail?.member_total ?? 0} members`"
    >
      <template #actions>
        <button type="button" class="btn btn-ghost btn-sm" @click="router.push('/admin/permissions')">
          All Groups
        </button>
      </template>
    </PageHeader>

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <div v-if="loading" class="py-10 text-xs text-faint">Reading group…</div>

    <div v-else-if="detail" class="grid gap-6 lg:grid-cols-2">
      <ConsolePanel title="Granted Permissions" :note="`${detail.node_total}`">
        <div class="mb-4 flex gap-2">
          <input
            v-model="permissionKey"
            class="input input-sm flex-1"
            placeholder="accounts.view"
            autocapitalize="off"
            spellcheck="false"
            @keydown.enter="grant"
          />
          <button
            type="button"
            class="btn btn-primary btn-sm"
            :disabled="busy || !permissionKey.trim()"
            @click="grant"
          >
            Grant
          </button>
        </div>

        <p v-if="!sortedNodes.length" class="voice py-4 text-[12px]">
          This group grants nothing yet. Add a permission key above.
        </p>

        <ul v-else class="max-h-[420px] divide-y divide-line-soft overflow-y-auto">
          <li
            v-for="node in sortedNodes"
            :key="node.id"
            class="flex items-start justify-between gap-3 py-2 first:pt-0"
          >
            <div class="min-w-0">
              <p class="break-all text-xs text-base-content">{{ node.key }}</p>
              <p class="mt-0.5 text-2xs text-faint">
                {{ actorTypeLabels[node.type] ?? 'actor' }} · {{ node.actor }}
                <template v-if="node.expired_at"> · {{ remaining(node.expired_at) }}</template>
              </p>
            </div>
            <button
              type="button"
              class="shrink-0 text-2xs text-dim hover:text-error"
              :disabled="busy"
              @click="pendingNode = node.key"
            >
              Revoke
            </button>
          </li>
        </ul>
      </ConsolePanel>

      <ConsolePanel title="Members" :note="`${detail.member_total}`">
        <div class="mb-4 flex gap-2">
          <input
            v-model="memberActor"
            class="input input-sm flex-1"
            placeholder="account id"
            autocapitalize="off"
            spellcheck="false"
            @keydown.enter="addMember"
          />
          <button
            type="button"
            class="btn btn-primary btn-sm"
            :disabled="busy || !memberActor.trim()"
            @click="addMember"
          >
            Add
          </button>
        </div>

        <p v-if="!detail.members.length" class="voice py-4 text-[12px]">
          No members. The seeded groups enroll accounts automatically.
        </p>

        <ul v-else class="max-h-[420px] divide-y divide-line-soft overflow-y-auto">
          <li
            v-for="member in detail.members"
            :key="member.actor"
            class="flex items-start justify-between gap-3 py-2 first:pt-0"
          >
            <div class="min-w-0">
              <p class="break-all text-xs text-base-content">{{ member.actor }}</p>
              <p class="mt-0.5 text-2xs text-faint">
                added {{ relative(member.created_at) }}
                <template v-if="member.expired_at"> · {{ remaining(member.expired_at) }}</template>
              </p>
            </div>
            <button
              type="button"
              class="shrink-0 text-2xs text-dim hover:text-error"
              :disabled="busy"
              @click="pendingMember = member.actor"
            >
              Remove
            </button>
          </li>
        </ul>
      </ConsolePanel>
    </div>

    <ConfirmAction
      v-if="pendingNode"
      title="Revoke This Permission?"
      :body="`${pendingNode} is removed from ${detail?.group.key}. Accounts in this group lose the grant on their next check.`"
      action="Revoke"
      tone="error"
      :busy="busy"
      @cancel="pendingNode = null"
      @confirm="revoke"
    />

    <ConfirmAction
      v-if="pendingMember"
      title="Remove This Member?"
      :body="`The account leaves ${detail?.group.key} and loses every permission it granted.`"
      action="Remove Member"
      tone="error"
      :busy="busy"
      @cancel="pendingMember = null"
      @confirm="removeMember"
    />
  </div>
</template>
