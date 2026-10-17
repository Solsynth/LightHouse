<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { TrashCan16, UserMinus16 } from '@carbon/icons-vue'
import { useRoute, useRouter } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { PermissionGroupDetail } from '@/lib/api/types'
import { actorTypeLabel } from '@/lib/enums'
import { relative, remaining } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import IconButton from '@/components/IconButton.vue'

const { t } = useI18n()
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
    error.value =
      caught instanceof StargateError ? caught.message : t('admin.permissionGroup.errorLoad')
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
    notice.ok(t('admin.permissionGroup.noticeGranted', { key: permissionKey.value.trim() }))
    permissionKey.value = ''
    await load()
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('admin.permissionGroup.errorGrant')
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
    notice.ok(t('admin.permissionGroup.noticeRevoked', { key }))
    pendingNode.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.permissionGroup.errorRevoke'))
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
    notice.ok(t('admin.permissionGroup.noticeMemberAdded'))
    memberActor.value = ''
    await load()
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('admin.permissionGroup.errorMemberAdd')
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
    notice.ok(t('admin.permissionGroup.noticeMemberRemoved'))
    pendingMember.value = null
    await load()
  } catch (caught) {
    notice.fail(
      caught instanceof Error ? caught.message : t('admin.permissionGroup.errorMemberRemove'),
    )
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
      :title="detail?.group.key ?? t('admin.permissionGroup.title')"
      :description="
        t('admin.permissionGroup.summary', {
          nodes: detail?.node_total ?? 0,
          members: detail?.member_total ?? 0,
        })
      "
    >
      <template #actions>
        <button type="button" class="btn btn-ghost btn-sm" @click="router.push('/admin/permissions')">
          {{ t('admin.permissionGroup.allGroups') }}
        </button>
      </template>
    </PageHeader>

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <LoadingSkeleton v-if="loading" compact :rows="6" :columns="[4, 3, 2, 1]" header />

    <div v-else-if="detail" class="grid gap-6 lg:grid-cols-2">
      <ConsolePanel
        :title="t('admin.permissionGroup.granted')"
        :note="`${detail.node_total}`"
      >
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
            {{ t('admin.permissionGroup.grant') }}
          </button>
        </div>

        <p v-if="!sortedNodes.length" class="voice py-4 text-[12px]">
          {{ t('admin.permissionGroup.emptyGrants') }}
        </p>

        <ul v-else class="max-h-[420px] overflow-y-auto">
          <li
            v-for="node in sortedNodes"
            :key="node.id"
            class="flex items-start justify-between gap-3 py-2 first:pt-0"
          >
            <div class="min-w-0">
              <p class="break-all text-xs text-base-content">{{ node.key }}</p>
              <p class="mt-0.5 text-2xs text-faint">
                {{ actorTypeLabel(node.type) }} · {{ node.actor }}
                <template v-if="node.expired_at"> · {{ remaining(node.expired_at) }}</template>
              </p>
            </div>
            <IconButton
              :label="t('admin.permissionGroup.revoke')"
              tone="danger"
              :disabled="busy"
              @click="pendingNode = node.key"
            >
              <TrashCan16 aria-hidden="true" />
            </IconButton>
          </li>
        </ul>
      </ConsolePanel>

      <ConsolePanel
        :title="t('admin.permissionGroup.members')"
        :note="`${detail.member_total}`"
      >
        <div class="mb-4 flex gap-2">
          <input
            v-model="memberActor"
            class="input input-sm flex-1"
            :placeholder="t('admin.permissionGroup.memberPlaceholder')"
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
            {{ t('common.add') }}
          </button>
        </div>

        <p v-if="!detail.members.length" class="voice py-4 text-[12px]">
          {{ t('admin.permissionGroup.emptyMembers') }}
        </p>

        <ul v-else class="max-h-[420px] overflow-y-auto">
          <li
            v-for="member in detail.members"
            :key="member.actor"
            class="flex items-start justify-between gap-3 py-2 first:pt-0"
          >
            <div class="min-w-0">
              <p class="break-all text-xs text-base-content">{{ member.actor }}</p>
              <p class="mt-0.5 text-2xs text-faint">
                {{ t('admin.permissionGroup.added', { time: relative(member.created_at) }) }}
                <template v-if="member.expired_at"> · {{ remaining(member.expired_at) }}</template>
              </p>
            </div>
            <IconButton
              :label="t('common.remove')"
              tone="danger"
              :disabled="busy"
              @click="pendingMember = member.actor"
            >
              <UserMinus16 aria-hidden="true" />
            </IconButton>
          </li>
        </ul>
      </ConsolePanel>
    </div>

    <ConfirmAction
      v-if="pendingNode"
      :title="t('admin.permissionGroup.confirmRevokeTitle')"
      :body="
        t('admin.permissionGroup.confirmRevokeBody', {
          node: pendingNode,
          group: detail?.group.key,
        })
      "
      :action="t('admin.permissionGroup.confirmRevokeAction')"
      tone="error"
      :busy="busy"
      @cancel="pendingNode = null"
      @confirm="revoke"
    />

    <ConfirmAction
      v-if="pendingMember"
      :title="t('admin.permissionGroup.confirmRemoveTitle')"
      :body="t('admin.permissionGroup.confirmRemoveBody', { group: detail?.group.key })"
      :action="t('admin.permissionGroup.confirmRemoveAction')"
      tone="error"
      :busy="busy"
      @cancel="pendingMember = null"
      @confirm="removeMember"
    />
  </div>
</template>
