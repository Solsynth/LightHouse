<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import { useNoticeStore } from '@/stores/notice'
import { appApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { ApiKey, ApiKeyCreated, AuthorizedApp, Connection } from '@/lib/api/types'
import { relative, remaining, shortId } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import CopyButton from '@/components/CopyButton.vue'

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
      caught instanceof StargateError ? caught.message : 'Developer resources could not be loaded.'
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
    notice.ok('API key issued. Copy it now; it is shown once.')
    await load()
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : 'The key could not be created.'
  } finally {
    busy.value = false
  }
}

async function rotate(key: ApiKey) {
  busy.value = true
  try {
    const rotated = await appApi.rotateKey(key.id)
    issued.value = { ...key, token: rotated.token }
    notice.ok('Key rotated. The previous token is invalid.')
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : 'The key could not be rotated.')
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
    notice.ok(`Revoked key ${target.label}.`)
    pendingRevoke.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : 'The key could not be revoked.')
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
    notice.ok(`Removed ${target.app_name ?? target.app_slug ?? 'app'}.`)
    pendingDeauth.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : 'The app could not be removed.')
  } finally {
    busy.value = false
  }
}

async function toggleVisibility(connection: Connection) {
  busy.value = true
  try {
    await appApi.setConnectionVisibility(connection.id, !connection.is_public)
    notice.ok(connection.is_public ? 'Connection hidden.' : 'Connection shown on your profile.')
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : 'Visibility could not be changed.')
  } finally {
    busy.value = false
  }
}

async function removeConnection(connection: Connection) {
  busy.value = true
  try {
    await appApi.removeConnection(connection.id)
    notice.ok('Connection removed.')
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : 'The connection could not be removed.')
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      title="Developers"
      description="Apps you authorized, keys that act as this account, and linked identities."
    />

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <div class="space-y-6">
      <ConsolePanel title="Authorized Apps" :note="`${apps.length} apps`">
        <p v-if="loading" class="py-4 text-xs text-faint">Reading authorizations…</p>
        <p v-else-if="!apps.length" class="voice py-4 text-[12px]">
          No app holds access to this account. Apps appear here after you approve an OAuth
          request.
        </p>

        <div v-else class="overflow-x-auto">
          <table class="ledger">
            <thead>
              <tr>
                <th scope="col">app</th>
                <th scope="col">scopes</th>
                <th scope="col">authorized</th>
                <th scope="col">last used</th>
                <th scope="col"><span class="sr-only">Actions</span></th>
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
                    Revoke
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <template v-if="scopes.length" #footer>
          <p class="text-2xs text-faint">
            Granted scope vocabulary: <span class="text-dim">{{ scopes.join(' · ') }}</span>
          </p>
        </template>
      </ConsolePanel>

      <ConsolePanel title="API Keys" :note="`${keys.length} keys`">
        <div class="mb-4 flex flex-wrap items-end gap-2">
          <div class="min-w-[200px] flex-1">
            <label for="key-label" class="field-label">Label</label>
            <input
              id="key-label"
              v-model="keyLabel"
              class="input input-sm"
              placeholder="deploy script"
              @keydown.enter="createKey"
            />
          </div>
          <button
            type="button"
            class="btn btn-primary btn-sm"
            :disabled="busy || !keyLabel.trim()"
            @click="createKey"
          >
            Issue Key
          </button>
        </div>

        <div v-if="issued" class="mb-4 border border-warning/50 bg-warning/5 p-3">
          <div class="flex items-center justify-between gap-2">
            <p class="text-2xs uppercase tracking-[0.12em] text-warning">token issued</p>
            <CopyButton :value="issued.token" label="token" />
          </div>
          <p class="mt-2 break-all font-mono text-xs text-base-content">{{ issued.token }}</p>
          <p class="voice mt-2 text-[12px] text-faint">
            This is the only time the token is shown. Store it in the secret manager the client
            uses.
          </p>
        </div>

        <p v-if="loading" class="py-4 text-xs text-faint">Reading keys…</p>
        <p v-else-if="!keys.length" class="voice py-4 text-[12px]">
          No API keys. Keys authenticate scripts and bots; they cannot use interactive endpoints.
        </p>

        <div v-else class="overflow-x-auto">
          <table class="ledger">
            <thead>
              <tr>
                <th scope="col">label</th>
                <th scope="col">id</th>
                <th scope="col">created</th>
                <th scope="col">expires</th>
                <th scope="col"><span class="sr-only">Actions</span></th>
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
                      Rotate
                    </button>
                    <button
                      type="button"
                      class="text-2xs text-dim hover:text-error"
                      :disabled="busy"
                      @click="pendingRevoke = key"
                    >
                      Revoke
                    </button>
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </ConsolePanel>

      <ConsolePanel title="Linked Identities" :note="`${connections.length} connections`">
        <p v-if="loading" class="py-4 text-xs text-faint">Reading connections…</p>
        <p v-else-if="!connections.length" class="voice py-4 text-[12px]">
          No linked identities. OIDC and social sign-ins appear here once connected.
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
                <span v-if="connection.is_public" class="chip text-accent">public</span>
                <span v-if="connection.registered_at" class="chip text-primary">
                  created account
                </span>
              </div>
              <p class="mt-1 text-2xs text-faint">last used {{ relative(connection.last_used_at) }}</p>
            </div>
            <div class="flex items-center gap-3">
              <button
                type="button"
                class="text-2xs text-dim hover:text-base-content"
                :disabled="busy"
                @click="toggleVisibility(connection)"
              >
                {{ connection.is_public ? 'Hide' : 'Show Publicly' }}
              </button>
              <button
                v-if="!connection.registered_at"
                type="button"
                class="text-2xs text-dim hover:text-error"
                :disabled="busy"
                @click="removeConnection(connection)"
              >
                Unlink
              </button>
            </div>
          </li>
        </ul>
      </ConsolePanel>
    </div>

    <ConfirmAction
      v-if="pendingRevoke"
      title="Revoke This Key?"
      :body="`${pendingRevoke.label} stops working immediately. Any client using it fails its next request.`"
      action="Revoke Key"
      tone="error"
      :busy="busy"
      @cancel="pendingRevoke = null"
      @confirm="revokeKey"
    />

    <ConfirmAction
      v-if="pendingDeauth"
      title="Remove This App?"
      :body="`${pendingDeauth.app_name ?? pendingDeauth.app_slug ?? 'The app'} loses its tokens and must ask for access again.`"
      action="Remove App"
      tone="error"
      :busy="busy"
      @cancel="pendingDeauth = null"
      @confirm="deauthorize"
    />
  </div>
</template>
