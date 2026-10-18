<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Add16, Renew16, TrashCan16 } from '@carbon/icons-vue'
import { RouterLink, useRouter } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { appApi, oauthAppApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { ApiKey, ApiKeyCreated, AuthorizedApp, OAuthApp } from '@/lib/api/types'
import { oauthAppStatusLabel } from '@/lib/enums'
import { relative, remaining, shortId } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import IconButton from '@/components/IconButton.vue'
import CopyButton from '@/components/CopyButton.vue'
import AppMark from '@/components/AppMark.vue'

const { t } = useI18n()
const router = useRouter()
const notice = useNoticeStore()

const apps = ref<AuthorizedApp[]>([])
const keys = ref<ApiKey[]>([])
const loading = ref(true)
const error = ref('')
const busy = ref(false)

// Apps this account registered as an OAuth client. They are the other half of
// the developer surface: the list above says who holds access to you, this one
// says who you are the identity provider for.
const owned = ref<OAuthApp[]>([])
const creating = ref(false)
const draft = ref({ name: '', slug: '', description: '' })
const createOpen = ref(false)

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
    const [appPage, appList, keyList] = await Promise.all([
      appApi.authorized(),
      oauthAppApi.list(),
      appApi.keys(),
    ])
    apps.value = appPage.items ?? []
    owned.value = appList ?? []
    keys.value = keyList ?? []
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('developers.errorLoad')
  } finally {
    loading.value = false
  }
}

async function createApp() {
  if (!draft.value.name.trim() || !draft.value.slug.trim()) return
  creating.value = true
  error.value = ''
  try {
    const created = await oauthAppApi.create({
      name: draft.value.name.trim(),
      slug: draft.value.slug.trim(),
      description: draft.value.description.trim(),
      // A new registration starts developing with the standard scope set, so
      // the client works immediately and is narrowed in the app's own page.
      status: 0,
      oauth_config: {
        allowed_scopes: ['openid', 'profile', 'email'],
        allowed_grant_types: ['authorization_code', 'refresh_token'],
        require_pkce: true,
      },
    })
    draft.value = { name: '', slug: '', description: '' }
    createOpen.value = false
    notice.ok(t('oauthApps.noticeCreated'))
    await router.push(`/developers/apps/${encodeURIComponent(created.id)}`)
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('oauthApps.errorCreate')
  } finally {
    creating.value = false
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
      <ConsolePanel
        :title="t('oauthApps.panelTitle')"
        :note="loading ? undefined : t('oauthApps.note', { n: owned.length })"
      >
        <template #header>
          <button
            type="button"
            class="btn btn-primary btn-xs"
            :aria-expanded="createOpen"
            @click="createOpen = !createOpen"
          >
            <Add16 aria-hidden="true" />
            {{ t('oauthApps.create') }}
          </button>
        </template>

        <!-- Registration starts with the three fields the provider needs to
             resolve a client at all; everything else is edited on the app. -->
        <form v-if="createOpen" class="mb-4 space-y-2 border border-line bg-base-200/40 p-3" @submit.prevent="createApp">
          <div class="grid gap-2 sm:grid-cols-2">
            <div>
              <label for="app-name" class="field-label">{{ t('oauthApps.name') }}</label>
              <input
                id="app-name"
                v-model="draft.name"
                class="input input-sm"
                :placeholder="t('oauthApps.namePlaceholder')"
                required
              />
            </div>
            <div>
              <label for="app-slug" class="field-label">{{ t('oauthApps.slug') }}</label>
              <input
                id="app-slug"
                v-model="draft.slug"
                class="input input-sm font-mono"
                :placeholder="t('oauthApps.slugPlaceholder')"
                pattern="[A-Za-z0-9._-]+"
                required
              />
            </div>
          </div>
          <div>
            <label for="app-description" class="field-label">{{ t('oauthApps.descriptionLabel') }}</label>
            <input
              id="app-description"
              v-model="draft.description"
              class="input input-sm"
              :placeholder="t('oauthApps.descriptionPlaceholder')"
            />
          </div>
          <div class="flex flex-wrap items-center justify-between gap-2">
            <p class="voice max-w-md text-[12px] text-faint">{{ t('oauthApps.createHint') }}</p>
            <button
              type="submit"
              class="btn btn-primary btn-sm"
              :disabled="creating || !draft.name.trim() || !draft.slug.trim()"
            >
              {{ creating ? t('oauthApps.creating') : t('oauthApps.submitCreate') }}
            </button>
          </div>
        </form>

        <LoadingSkeleton v-if="loading" :rows="3" :columns="[1, 3, 2, 1]" header />
        <p v-else-if="!owned.length" class="voice py-4 text-[12px]">
          {{ t('oauthApps.empty') }}
        </p>

        <div v-else class="table-bleed overflow-x-auto">
          <table class="ledger">
            <thead>
              <tr>
                <th scope="col">{{ t('oauthApps.app') }}</th>
                <th scope="col">{{ t('oauthApps.status') }}</th>
                <th scope="col">{{ t('oauthApps.scopes') }}</th>
                <th scope="col">{{ t('oauthApps.updated') }}</th>
                <th scope="col"><span class="sr-only">{{ t('common.actions') }}</span></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="app in owned" :key="app.id">
                <td>
                  <div class="flex items-center gap-2">
                    <AppMark :picture="app.picture" :name="app.name" :size="24" flat />
                    <div class="min-w-0">
                      <p class="truncate text-xs text-base-content">{{ app.name }}</p>
                      <p class="truncate text-2xs text-faint">{{ app.slug }}</p>
                    </div>
                  </div>
                </td>
                <td>
                  <span class="chip" :class="app.status === 2 ? 'text-success' : ''">
                    {{ oauthAppStatusLabel(app.status) }}
                  </span>
                </td>
                <td class="max-w-[220px]">
                  <span class="flex flex-wrap gap-1">
                    <span
                      v-for="scope in app.oauth_config.allowed_scopes"
                      :key="scope"
                      class="chip"
                    >{{ scope }}</span>
                  </span>
                </td>
                <td class="text-xs text-dim">{{ relative(app.updated_at ?? app.created_at) }}</td>
                <td>
                  <RouterLink
                    :to="`/developers/apps/${encodeURIComponent(app.id)}`"
                    class="btn btn-ghost btn-xs"
                  >
                    {{ t('oauthApps.open') }}
                  </RouterLink>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </ConsolePanel>

      <ConsolePanel :title="t('developers.authorizedApps')" :note="loading ? undefined : t('developers.appsNote', { n: apps.length })">
        <LoadingSkeleton v-if="loading" :rows="3" :columns="[3, 2, 1, 1]" header />
        <p v-else-if="!apps.length" class="voice py-4 text-[12px]">
          {{ t('developers.noApps') }}
        </p>

        <div v-else class="table-bleed overflow-x-auto">
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
                  <IconButton
                    :label="t('developers.revoke')"
                    tone="danger"
                    :disabled="busy"
                    @click="pendingDeauth = app"
                  >
                    <TrashCan16 aria-hidden="true" />
                  </IconButton>
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

      <ConsolePanel :title="t('developers.apiKeys')" :note="loading ? undefined : t('developers.keysNote', { n: keys.length })">
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

        <LoadingSkeleton v-if="loading" :rows="3" :columns="[2, 4, 1, 1]" header />
        <p v-else-if="!keys.length" class="voice py-4 text-[12px]">
          {{ t('developers.noKeys') }}
        </p>

        <div v-else class="table-bleed overflow-x-auto">
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
                  <span class="flex items-center justify-end gap-1">
                    <IconButton
                      :label="t('developers.rotate')"
                      :disabled="busy"
                      @click="rotate(key)"
                    >
                      <Renew16 aria-hidden="true" />
                    </IconButton>
                    <IconButton
                      :label="t('developers.revoke')"
                      tone="danger"
                      :disabled="busy"
                      @click="pendingRevoke = key"
                    >
                      <TrashCan16 aria-hidden="true" />
                    </IconButton>
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
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
