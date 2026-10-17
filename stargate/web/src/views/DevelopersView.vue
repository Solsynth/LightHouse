<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { useNoticeStore } from '@/stores/notice'
import { appApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { ApiKey, ApiKeyCreated, AuthorizedApp, Connection } from '@/lib/api/types'
import { relative, remaining, shortId } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import CopyButton from '@/components/CopyButton.vue'

const { t } = useI18n()
const notice = useNoticeStore()

const apps = ref<AuthorizedApp[]>([])
const keys = ref<ApiKey[]>([])
const connections = ref<Connection[]>([])
const loading = ref(true)
const error = ref('')
const busy = ref(false)

const keyLabel = ref('')
const issued = ref<ApiKeyCreated | null>(null)
const pendingRevoke = ref<ApiKey | null>(null)
const pendingDeauth = ref<AuthorizedApp | null>(null)

const scopes = computed(() => {
  const unique = new Set<string>()
  for (const app of apps.value) for (const scope of app.scopes ?? []) unique.add(scope)
  return [...unique].sort()
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [appPage, keyList, connectionList] = await Promise.all([
      appApi.authorized(),
      appApi.keys(),
      appApi.connections(),
    ])
    apps.value = appPage.items ?? []
    keys.value = keyList ?? []
    connections.value = connectionList ?? []
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('developers.errorLoad')
  } finally {
    loading.value = false
  }
}

async function createKey() {
  if (!keyLabel.value.trim()) return
  busy.value = true
  error.value = ''
  try {
    issued.value = await appApi.createKey(keyLabel.value.trim())
    keyLabel.value = ''
    notice.ok(t('developers.noticeKeyIssued'))
    await load()
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('developers.errorCreate')
  } finally {
    busy.value = false
  }
}

async function rotate(key: ApiKey) {
  busy.value = true
  try {
    const rotated = await appApi.rotateKey(key.id)
    issued.value = { ...key, token: rotated.token }
    notice.ok(t('developers.noticeRotated'))
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('developers.errorRotate'))
  } finally {
    busy.value = false
  }
}

async function revokeKey() {
  const target = pendingRevoke.value
  if (!target) return
  busy.value = true
  try {
    await appApi.revokeKey(target.id)
    notice.ok(t('developers.noticeKeyRevoked', { label: target.label }))
    pendingRevoke.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('developers.errorRevoke'))
  } finally {
    busy.value = false
  }
}

async function deauthorize() {
  const target = pendingDeauth.value
  if (!target) return
  busy.value = true
  try {
    await appApi.deauthorize(target.id, target.type)
    notice.ok(
      t('developers.noticeAppRemoved', {
        app: target.app_name ?? target.app_slug ?? t('developers.defaultAppName'),
      }),
    )
    pendingDeauth.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('developers.errorDeauthorize'))
  } finally {
    busy.value = false
  }
}

async function toggleVisibility(connection: Connection) {
  busy.value = true
  try {
    await appApi.setConnectionVisibility(connection.id, !connection.is_public)
    notice.ok(
      connection.is_public
        ? t('developers.noticeConnectionHidden')
        : t('developers.noticeConnectionShown'),
    )
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('developers.errorVisibility'))
  } finally {
    busy.value = false
  }
}

async function removeConnection(connection: Connection) {
  busy.value = true
  try {
    await appApi.removeConnection(connection.id)
    notice.ok(t('developers.noticeConnectionRemoved'))
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('developers.errorRemoveConnection'))
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      :title="t('developers.title')"
      :description="t('developers.description')"
    />

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <div class="space-y-6">
      <ConsolePanel :title="t('developers.authorizedApps')" :note="t('developers.appsNote', { n: apps.length })">
        <p v-if="loading" class="py-4 text-xs text-faint">{{ t('developers.readingAuthorizations') }}</p>
        <p v-else-if="!apps.length" class="voice py-4 text-[12px]">
          {{ t('developers.noApps') }}
        </p>

        <div v-else class="overflow-x-auto">
          <table class="ledger">
            <thead>
              <tr>
                <th scope="col">{{ t('developers.app') }}</th>
                <th scope="col">{{ t('developers.scopes') }}</th>
                <th scope="col">{{ t('developers.authorized') }}</th>
                <th scope="col">{{ t('common.lastUsed') }}</th>
                <th scope="col"><span class="sr-only">{{ t('common.actions') }}</span></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="app in apps" :key="app.id">
                <td>
                  <p class="text-xs text-base-content">{{ app.app_name ?? app.app_slug ?? app.app_id }}</p>
                  <p class="text-2xs text-faint" :title="app.app_id">{{ shortId(app.app_id) }}</p>
                </td>
                <td class="max-w-[240px]">
                  <span class="flex flex-wrap gap-1">
                    <span v-for="scope in app.scopes" :key="scope" class="chip">{{ scope }}</span>
                  </span>
                </td>
                <td class="text-xs text-dim">{{ relative(app.last_authorized_at) }}</td>
                <td class="text-xs text-dim">{{ relative(app.last_used_at) }}</td>
                <td>
                  <button
                    type="button"
                    class="text-2xs text-dim hover:text-error"
                    :disabled="busy"
                    @click="pendingDeauth = app"
                  >
                    {{ t('developers.revoke') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <template v-if="scopes.length" #footer>
          <p class="text-2xs text-faint">
            {{ t('developers.grantedScopes') }} <span class="text-dim">{{ scopes.join(' · ') }}</span>
          </p>
        </template>
      </ConsolePanel>

      <ConsolePanel :title="t('developers.apiKeys')" :note="t('developers.keysNote', { n: keys.length })">
        <div class="mb-4 flex flex-wrap items-end gap-2">
          <div class="min-w-[200px] flex-1">
            <label for="key-label" class="field-label">{{ t('developers.label') }}</label>
            <input
              id="key-label"
              v-model="keyLabel"
              class="input input-sm"
              :placeholder="t('developers.labelPlaceholder')"
              @keydown.enter="createKey"
            />
          </div>
          <button
            type="button"
            class="btn btn-primary btn-sm"
            :disabled="busy || !keyLabel.trim()"
            @click="createKey"
          >
            {{ t('developers.issueKey') }}
          </button>
        </div>

        <div v-if="issued" class="mb-4 border border-warning/50 bg-warning/5 p-3">
          <div class="flex items-center justify-between gap-2">
            <p class="text-2xs uppercase tracking-[0.12em] text-warning">{{ t('developers.tokenIssued') }}</p>
            <CopyButton :value="issued.token" :label="t('developers.token')" />
          </div>
          <p class="mt-2 break-all font-mono text-xs text-base-content">{{ issued.token }}</p>
          <p class="voice mt-2 text-[12px] text-faint">
            {{ t('developers.tokenHint') }}
          </p>
        </div>

        <p v-if="loading" class="py-4 text-xs text-faint">{{ t('developers.readingKeys') }}</p>
        <p v-else-if="!keys.length" class="voice py-4 text-[12px]">
          {{ t('developers.noKeys') }}
        </p>

        <div v-else class="overflow-x-auto">
          <table class="ledger">
            <thead>
              <tr>
                <th scope="col">{{ t('developers.label') }}</th>
                <th scope="col">{{ t('common.id') }}</th>
                <th scope="col">{{ t('common.created') }}</th>
                <th scope="col">{{ t('common.expires') }}</th>
                <th scope="col"><span class="sr-only">{{ t('common.actions') }}</span></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="key in keys" :key="key.id">
                <td class="text-xs text-base-content">{{ key.label }}</td>
                <td class="text-xs text-dim" :title="key.id">{{ shortId(key.id) }}</td>
                <td class="text-xs text-dim">{{ relative(key.created_at) }}</td>
                <td class="text-xs text-dim">{{ remaining(key.expired_at) }}</td>
                <td>
                  <span class="flex items-center justify-end gap-3">
                    <button
                      type="button"
                      class="text-2xs text-dim hover:text-base-content"
                      :disabled="busy"
                      @click="rotate(key)"
                    >
                      {{ t('developers.rotate') }}
                    </button>
                    <button
                      type="button"
                      class="text-2xs text-dim hover:text-error"
                      :disabled="busy"
                      @click="pendingRevoke = key"
                    >
                      {{ t('developers.revoke') }}
                    </button>
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </ConsolePanel>

      <ConsolePanel
        :title="t('developers.linkedIdentities')"
        :note="t('developers.connectionsNote', { n: connections.length })"
      >
        <p v-if="loading" class="py-4 text-xs text-faint">{{ t('developers.readingConnections') }}</p>
        <p v-else-if="!connections.length" class="voice py-4 text-[12px]">
          {{ t('developers.noConnections') }}
        </p>

        <ul v-else class="divide-y divide-line-soft">
          <li
            v-for="connection in connections"
            :key="connection.id"
            class="flex flex-wrap items-center justify-between gap-3 py-3 first:pt-0 last:pb-0"
          >
            <div>
              <div class="flex flex-wrap items-center gap-2">
                <span class="text-xs text-base-content">{{ connection.provider }}</span>
                <span class="chip">{{ connection.provided_identifier }}</span>
                <span v-if="connection.is_public" class="chip text-accent">{{ t('developers.public') }}</span>
                <span v-if="connection.registered_at" class="chip text-primary">
                  {{ t('developers.createdAccount') }}
                </span>
              </div>
              <p class="mt-1 text-2xs text-faint">
                {{ t('developers.lastUsedAt', { time: relative(connection.last_used_at) }) }}
              </p>
            </div>
            <div class="flex items-center gap-3">
              <button
                type="button"
                class="text-2xs text-dim hover:text-base-content"
                :disabled="busy"
                @click="toggleVisibility(connection)"
              >
                {{ connection.is_public ? t('developers.hide') : t('developers.showPublicly') }}
              </button>
              <button
                v-if="!connection.registered_at"
                type="button"
                class="text-2xs text-dim hover:text-error"
                :disabled="busy"
                @click="removeConnection(connection)"
              >
                {{ t('developers.unlink') }}
              </button>
            </div>
          </li>
        </ul>
      </ConsolePanel>
    </div>

    <ConfirmAction
      v-if="pendingRevoke"
      :title="t('developers.revokeKeyTitle')"
      :body="t('developers.revokeKeyBody', { label: pendingRevoke.label })"
      :action="t('developers.revokeKeyAction')"
      tone="error"
      :busy="busy"
      @cancel="pendingRevoke = null"
      @confirm="revokeKey"
    />

    <ConfirmAction
      v-if="pendingDeauth"
      :title="t('developers.removeAppTitle')"
      :body="t('developers.removeAppBody', {
        app: pendingDeauth.app_name ?? pendingDeauth.app_slug ?? t('developers.defaultAppName'),
      })"
      :action="t('developers.removeAppAction')"
      tone="error"
      :busy="busy"
      @cancel="pendingDeauth = null"
      @confirm="deauthorize"
    />
  </div>
</template>
