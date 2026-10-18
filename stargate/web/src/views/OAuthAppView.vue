<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Add16, Renew16, TrashCan16 } from '@carbon/icons-vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { useSessionStore } from '@/stores/session'
import { oauthAppApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { OAuthApp, OAuthAppSecret } from '@/lib/api/types'
import { oauthAppStatusLabel } from '@/lib/enums'
import { relative, remaining, shortId, utcStamp } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import IconButton from '@/components/IconButton.vue'
import CopyButton from '@/components/CopyButton.vue'
import AppMark from '@/components/AppMark.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const notice = useNoticeStore()
const session = useSessionStore()

const app = ref<OAuthApp | null>(null)
const secrets = ref<OAuthAppSecret[]>([])
const loading = ref(true)
const error = ref('')
const busy = ref(false)

const appId = computed(() => String(route.params.appId ?? ''))

/** Editable mirror of the app; saved as one patch, never field by field. */
const form = ref({
  name: '',
  slug: '',
  description: '',
  status: 0,
  clientUri: '',
  redirectUris: '',
  postLogoutUris: '',
  scopes: [] as string[],
  grants: [] as string[],
  requirePkce: true,
  allowOfflineAccess: false,
  publicClient: false,
  homePage: '',
  privacyPolicy: '',
  termsOfService: '',
})

const customScope = ref('')
const pictureId = ref('')
const backgroundId = ref('')

const secretDraft = ref({ description: '', expiresIn: '720h', type: 'oidc' as 'oidc' | 'api_key' })
const issued = ref<OAuthAppSecret | null>(null)
const pendingSecret = ref<OAuthAppSecret | null>(null)
const confirmDelete = ref(false)

/**
 * The scope and grant vocabularies the provider understands. A custom scope is
 * allowed because a deployment may define its own; these four are the ones the
 * console can explain.
 */
const knownScopes = ['openid', 'profile', 'email', 'offline_access', '*']
const knownGrants = ['authorization_code', 'refresh_token', 'device_code']

const scopeHint = computed<Record<string, string>>(() => ({
  openid: t('oauthApps.scopeOpenid'),
  profile: t('oauthApps.scopeProfile'),
  email: t('oauthApps.scopeEmail'),
  offline_access: t('oauthApps.scopeOfflineAccess'),
  '*': t('oauthApps.scopeAll'),
}))

const grantHint = computed<Record<string, string>>(() => ({
  authorization_code: t('oauthApps.grantAuthorizationCode'),
  refresh_token: t('oauthApps.grantRefreshToken'),
  device_code: t('oauthApps.grantDeviceCode'),
}))

const allScopes = computed(() => {
  const set = new Set([...knownScopes, ...(app.value?.oauth_config.allowed_scopes ?? []), ...form.value.scopes])
  return [...set]
})

const allGrants = computed(() => {
  const set = new Set([...knownGrants, ...(app.value?.oauth_config.allowed_grant_types ?? []), ...form.value.grants])
  return [...set]
})

/** A production client must name at least one redirect target before saving. */
const productionWithoutRedirect = computed(
  () => form.value.status === 2 && !form.value.redirectUris.trim(),
)

function lines(value: string): string[] {
  return value
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
}

function loadForm(value: OAuthApp) {
  form.value = {
    name: value.name,
    slug: value.slug,
    description: value.description ?? '',
    status: value.status,
    clientUri: value.oauth_config.client_uri ?? '',
    redirectUris: (value.oauth_config.redirect_uris ?? []).join('\n'),
    postLogoutUris: (value.oauth_config.post_logout_redirect_uris ?? []).join('\n'),
    scopes: [...(value.oauth_config.allowed_scopes ?? [])],
    grants: [...(value.oauth_config.allowed_grant_types ?? [])],
    requirePkce: value.oauth_config.require_pkce,
    allowOfflineAccess: value.oauth_config.allow_offline_access,
    publicClient: value.oauth_config.is_public_client,
    homePage: value.links?.home_page ?? '',
    privacyPolicy: value.links?.privacy_policy ?? '',
    termsOfService: value.links?.terms_of_service ?? '',
  }
  pictureId.value = value.picture?.id ?? ''
  backgroundId.value = value.background?.id ?? ''
  error.value = ''
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [detail, list] = await Promise.all([
      oauthAppApi.get(appId.value),
      oauthAppApi.secrets(appId.value),
    ])
    app.value = detail
    secrets.value = list ?? []
    loadForm(detail)
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('oauthApps.errorApp')
  } finally {
    loading.value = false
  }
}

function toggle(list: string[], value: string) {
  const index = list.indexOf(value)
  if (index >= 0) list.splice(index, 1)
  else list.push(value)
}

function addScope() {
  const value = customScope.value.trim()
  if (!value || form.value.scopes.includes(value)) return
  form.value.scopes.push(value)
  customScope.value = ''
}

async function save() {
  busy.value = true
  error.value = ''
  try {
    // Art travels only when it changed: an omitted id leaves the stored
    // reference alone, while the empty string asks the server to clear it.
    const body: Parameters<typeof oauthAppApi.update>[1] = {
      name: form.value.name.trim(),
      slug: form.value.slug.trim(),
      description: form.value.description,
      status: form.value.status,
      oauth_config: {
        client_uri: form.value.clientUri.trim(),
        redirect_uris: lines(form.value.redirectUris),
        post_logout_redirect_uris: lines(form.value.postLogoutUris),
        allowed_scopes: form.value.scopes,
        allowed_grant_types: form.value.grants,
        require_pkce: form.value.requirePkce,
        allow_offline_access: form.value.allowOfflineAccess,
        is_public_client: form.value.publicClient,
      },
      links: {
        home_page: form.value.homePage.trim(),
        privacy_policy: form.value.privacyPolicy.trim(),
        terms_of_service: form.value.termsOfService.trim(),
      },
    }
    if (pictureId.value !== (app.value?.picture?.id ?? '')) body.picture_id = pictureId.value.trim()
    if (backgroundId.value !== (app.value?.background?.id ?? '')) {
      body.background_id = backgroundId.value.trim()
    }
    app.value = await oauthAppApi.update(appId.value, body)
    loadForm(app.value)
    notice.ok(t('oauthApps.noticeSaved'))
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('oauthApps.errorSave')
  } finally {
    busy.value = false
  }
}

async function issueSecret() {
  busy.value = true
  error.value = ''
  try {
    issued.value = await oauthAppApi.createSecret(appId.value, {
      description: secretDraft.value.description.trim(),
      // "Never" is the provider's own default: omitting the field means no
      // expiry, and a numeric or duration value sets one.
      expires_in: secretDraft.value.expiresIn || undefined,
      type: secretDraft.value.type,
    })
    secretDraft.value = { description: '', expiresIn: '720h', type: 'oidc' }
    notice.ok(t('oauthApps.noticeSecretIssued'))
    await loadSecrets()
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('oauthApps.errorSecret')
  } finally {
    busy.value = false
  }
}

async function loadSecrets() {
  secrets.value = (await oauthAppApi.secrets(appId.value)) ?? []
}

async function rotateSecret(secret: OAuthAppSecret) {
  busy.value = true
  try {
    issued.value = await oauthAppApi.rotateSecret(appId.value, secret.id, {
      description: secret.description,
    })
    notice.ok(t('oauthApps.noticeRotated'))
    await loadSecrets()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('oauthApps.errorRotate'))
  } finally {
    busy.value = false
  }
}

async function revokeSecret() {
  const target = pendingSecret.value
  if (!target) return
  busy.value = true
  try {
    await oauthAppApi.removeSecret(appId.value, target.id)
    notice.ok(t('oauthApps.noticeRevoked'))
    pendingSecret.value = null
    await loadSecrets()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('oauthApps.errorRevoke'))
  } finally {
    busy.value = false
  }
}

async function remove() {
  busy.value = true
  try {
    await oauthAppApi.remove(appId.value)
    notice.ok(t('oauthApps.noticeDeleted'))
    confirmDelete.value = false
    await router.push('/developers')
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('oauthApps.errorDelete')
  } finally {
    busy.value = false
  }
}

onMounted(() => {
  // The session must resolve before a gated call: the delete and secret routes
  // need a real account, and the elevation sheet needs the session store.
  if (session.loading) void session.load()
  void load()
})
</script>

<template>
  <div>
    <PageHeader :title="app?.name || t('oauthApps.detailTitle')" :description="app?.description || t('oauthApps.description')">
      <template #actions>
        <RouterLink to="/developers" class="btn btn-ghost btn-sm">
          {{ t('oauthApps.backToApps') }}
        </RouterLink>
      </template>
    </PageHeader>

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <LoadingSkeleton v-if="loading" :rows="5" :columns="[2, 4, 3]" />

    <div v-else-if="app" class="space-y-6">
      <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
        <div class="space-y-6">
          <ConsolePanel :title="t('oauthApps.identity')" :note="oauthAppStatusLabel(app.status)">
            <p class="voice mb-4 text-[12px]">{{ t('oauthApps.identityHint') }}</p>

            <div class="flex flex-wrap items-center gap-3">
              <AppMark :picture="app.picture" :name="form.name || app.name" :size="52" />
              <div class="min-w-0 flex-1">
                <p class="text-2xs text-faint">{{ t('oauthApps.clientId') }}</p>
                <p class="flex items-center gap-2">
                  <span class="break-all font-mono text-xs text-base-content">{{ app.slug }}</span>
                  <CopyButton :value="app.slug" :label="t('oauthApps.clientId')" />
                </p>
                <p class="mt-1 text-2xs text-faint" :title="app.id">
                  {{ t('oauthApps.created') }} {{ utcStamp(app.created_at).slice(0, 10) }} ·
                  {{ shortId(app.id) }}
                </p>
              </div>
            </div>

            <div class="mt-5 grid gap-3 sm:grid-cols-2">
              <div>
                <label for="app-name" class="field-label">{{ t('oauthApps.name') }}</label>
                <input id="app-name" v-model="form.name" class="input input-sm" />
              </div>
              <div>
                <label for="app-slug-field" class="field-label">{{ t('oauthApps.slug') }}</label>
                <input id="app-slug-field" v-model="form.slug" class="input input-sm font-mono" />
                <p class="mt-1 text-2xs text-faint">{{ t('oauthApps.slugHint') }}</p>
              </div>
            </div>

            <div class="mt-3">
              <label for="app-description-field" class="field-label">{{ t('oauthApps.descriptionLabel') }}</label>
              <textarea id="app-description-field" v-model="form.description" class="textarea textarea-sm" rows="2" />
            </div>

            <div class="mt-3">
              <label for="app-status" class="field-label">{{ t('oauthApps.status') }}</label>
              <select id="app-status" v-model.number="form.status" class="select select-sm max-w-[220px]">
                <option v-for="value in [0, 1, 2, 3]" :key="value" :value="value">
                  {{ oauthAppStatusLabel(value) }}
                </option>
              </select>
            </div>

            <div class="mt-5 grid gap-3 sm:grid-cols-2">
              <div>
                <label for="app-picture" class="field-label">{{ t('oauthApps.pictureId') }}</label>
                <div class="flex gap-2">
                  <input id="app-picture" v-model="pictureId" class="input input-sm font-mono" />
                  <button type="button" class="btn btn-ghost btn-sm" @click="pictureId = ''">
                    {{ t('oauthApps.clearArt') }}
                  </button>
                </div>
                <p class="mt-1 text-2xs text-faint">{{ t('oauthApps.pictureIdHint') }}</p>
              </div>
              <div>
                <label for="app-background" class="field-label">{{ t('oauthApps.backgroundId') }}</label>
                <div class="flex gap-2">
                  <input id="app-background" v-model="backgroundId" class="input input-sm font-mono" />
                  <button type="button" class="btn btn-ghost btn-sm" @click="backgroundId = ''">
                    {{ t('oauthApps.clearArt') }}
                  </button>
                </div>
                <p class="mt-1 text-2xs text-faint">{{ t('oauthApps.backgroundIdHint') }}</p>
              </div>
            </div>
          </ConsolePanel>

          <ConsolePanel :title="t('oauthApps.oauth')" :note="t('oauthApps.oauthHint')">
            <div>
              <label for="app-client-uri" class="field-label">{{ t('oauthApps.clientUri') }}</label>
              <input id="app-client-uri" v-model="form.clientUri" class="input input-sm" placeholder="https://client.example" />
            </div>

            <div class="mt-4 grid gap-3 sm:grid-cols-2">
              <div>
                <label for="app-redirects" class="field-label">{{ t('oauthApps.redirectUris') }}</label>
                <textarea id="app-redirects" v-model="form.redirectUris" class="textarea textarea-sm font-mono" rows="4" placeholder="https://client.example/oauth/callback" />
                <p class="mt-1 text-2xs" :class="productionWithoutRedirect ? 'text-warning' : 'text-faint'">
                  {{ productionWithoutRedirect ? t('oauthApps.createRedirectHint') : t('oauthApps.redirectUrisHint') }}
                </p>
              </div>
              <div>
                <label for="app-logout" class="field-label">{{ t('oauthApps.postLogoutUris') }}</label>
                <textarea id="app-logout" v-model="form.postLogoutUris" class="textarea textarea-sm font-mono" rows="4" placeholder="https://client.example/logout" />
                <p class="mt-1 text-2xs text-faint">{{ t('oauthApps.postLogoutUrisHint') }}</p>
              </div>
            </div>

            <div class="mt-5">
              <p class="field-label">{{ t('oauthApps.allowedScopes') }}</p>
              <ul class="space-y-1.5">
                <li v-for="scope in allScopes" :key="scope">
                  <label class="flex cursor-pointer items-start gap-2 text-xs">
                    <input
                      type="checkbox"
                      class="checkbox checkbox-xs mt-0.5"
                      :checked="form.scopes.includes(scope)"
                      @change="toggle(form.scopes, scope)"
                    />
                    <span>
                      <span class="font-mono text-base-content">{{ scope }}</span>
                      <span v-if="scopeHint[scope]" class="ml-2 text-faint">{{ scopeHint[scope] }}</span>
                    </span>
                  </label>
                </li>
              </ul>
              <div class="mt-2 flex flex-wrap items-end gap-2">
                <div class="min-w-[180px] flex-1">
                  <label for="app-custom-scope" class="field-label">{{ t('oauthApps.customScope') }}</label>
                  <input
                    id="app-custom-scope"
                    v-model="customScope"
                    class="input input-sm font-mono"
                    :placeholder="t('oauthApps.customScopePlaceholder')"
                    @keydown.enter.prevent="addScope"
                  />
                </div>
                <button type="button" class="btn btn-ghost btn-sm" :disabled="!customScope.trim()" @click="addScope">
                  <Add16 aria-hidden="true" />
                  {{ t('oauthApps.addScope') }}
                </button>
              </div>
            </div>

            <div class="mt-5">
              <p class="field-label">{{ t('oauthApps.grantTypes') }}</p>
              <ul class="space-y-1.5">
                <li v-for="grant in allGrants" :key="grant">
                  <label class="flex cursor-pointer items-start gap-2 text-xs">
                    <input
                      type="checkbox"
                      class="checkbox checkbox-xs mt-0.5"
                      :checked="form.grants.includes(grant)"
                      @change="toggle(form.grants, grant)"
                    />
                    <span>
                      <span class="font-mono text-base-content">{{ grant }}</span>
                      <span v-if="grantHint[grant]" class="ml-2 text-faint">{{ grantHint[grant] }}</span>
                    </span>
                  </label>
                </li>
              </ul>
            </div>

            <ul class="mt-5 space-y-2">
              <li>
                <label class="flex cursor-pointer items-start gap-2 text-xs">
                  <input v-model="form.requirePkce" type="checkbox" class="checkbox checkbox-xs mt-0.5" />
                  <span>
                    <span class="text-base-content">{{ t('oauthApps.requirePkce') }}</span>
                    <span class="ml-2 text-faint">{{ t('oauthApps.requirePkceHint') }}</span>
                  </span>
                </label>
              </li>
              <li>
                <label class="flex cursor-pointer items-start gap-2 text-xs">
                  <input v-model="form.allowOfflineAccess" type="checkbox" class="checkbox checkbox-xs mt-0.5" />
                  <span>
                    <span class="text-base-content">{{ t('oauthApps.allowOfflineAccess') }}</span>
                    <span class="ml-2 text-faint">{{ t('oauthApps.allowOfflineAccessHint') }}</span>
                  </span>
                </label>
              </li>
              <li>
                <label class="flex cursor-pointer items-start gap-2 text-xs">
                  <input v-model="form.publicClient" type="checkbox" class="checkbox checkbox-xs mt-0.5" />
                  <span>
                    <span class="text-base-content">{{ t('oauthApps.publicClient') }}</span>
                    <span class="ml-2 text-faint">{{ t('oauthApps.publicClientHint') }}</span>
                  </span>
                </label>
              </li>
            </ul>
          </ConsolePanel>

          <ConsolePanel :title="t('oauthApps.links')" :note="t('oauthApps.linksHint')">
            <div class="grid gap-3 sm:grid-cols-3">
              <div>
                <label for="app-home" class="field-label">{{ t('oauthApps.homePage') }}</label>
                <input id="app-home" v-model="form.homePage" class="input input-sm" placeholder="https://client.example" />
              </div>
              <div>
                <label for="app-privacy" class="field-label">{{ t('oauthApps.privacyPolicy') }}</label>
                <input id="app-privacy" v-model="form.privacyPolicy" class="input input-sm" />
              </div>
              <div>
                <label for="app-terms" class="field-label">{{ t('oauthApps.termsOfService') }}</label>
                <input id="app-terms" v-model="form.termsOfService" class="input input-sm" />
              </div>
            </div>
          </ConsolePanel>

          <div class="flex flex-wrap items-center gap-2">
            <button type="button" class="btn btn-primary btn-sm" :disabled="busy" @click="save">
              {{ busy ? t('oauthApps.saving') : t('oauthApps.save') }}
            </button>
            <span class="text-2xs text-faint">{{ t('oauthApps.savedSecretsHint') }}</span>
          </div>
        </div>

        <div class="space-y-6">
          <ConsolePanel
            :title="t('oauthApps.secrets')"
            :note="t('oauthApps.secretsNote', { n: secrets.length })"
          >
            <p class="voice mb-3 text-[12px]">{{ t('oauthApps.secretsHint') }}</p>

            <div v-if="issued" class="mb-4 border border-warning/50 bg-warning/5 p-3">
              <div class="flex items-center justify-between gap-2">
                <p class="text-2xs uppercase tracking-[0.12em] text-warning">{{ t('oauthApps.secretIssued') }}</p>
                <CopyButton v-if="issued.secret" :value="issued.secret" :label="t('oauthApps.secretSecret')" />
              </div>
              <p v-if="issued.secret" class="mt-2 break-all font-mono text-xs text-base-content">{{ issued.secret }}</p>
              <p class="voice mt-2 text-[12px] text-faint">{{ t('oauthApps.secretIssuedHint') }}</p>
            </div>

            <div class="space-y-2 border border-line p-3">
              <div>
                <label for="secret-description" class="field-label">{{ t('oauthApps.secretDescription') }}</label>
                <input
                  id="secret-description"
                  v-model="secretDraft.description"
                  class="input input-sm"
                  :placeholder="t('oauthApps.secretDescriptionPlaceholder')"
                />
              </div>
              <div class="grid gap-2 sm:grid-cols-2">
                <div>
                  <label for="secret-expiry" class="field-label">{{ t('oauthApps.secretExpiry') }}</label>
                  <select id="secret-expiry" v-model="secretDraft.expiresIn" class="select select-sm">
                    <option value="">{{ t('oauthApps.expiryNever') }}</option>
                    <option value="720h">{{ t('oauthApps.expiry30d') }}</option>
                    <option value="2160h">{{ t('oauthApps.expiry90d') }}</option>
                    <option value="8760h">{{ t('oauthApps.expiry1y') }}</option>
                  </select>
                </div>
                <div>
                  <label for="secret-type" class="field-label">{{ t('oauthApps.secretType') }}</label>
                  <select id="secret-type" v-model="secretDraft.type" class="select select-sm">
                    <option value="oidc">{{ t('oauthApps.typeOidc') }}</option>
                    <option value="api_key">{{ t('oauthApps.typeApiKey') }}</option>
                  </select>
                </div>
              </div>
              <button type="button" class="btn btn-primary btn-sm w-full" :disabled="busy" @click="issueSecret">
                {{ busy ? t('oauthApps.issuing') : t('oauthApps.issueSecret') }}
              </button>
            </div>

            <p v-if="!secrets.length" class="voice mt-3 text-[12px]">{{ t('oauthApps.noSecrets') }}</p>
            <ul v-else class="mt-3">
              <li
                v-for="secret in secrets"
                :key="secret.id"
                class="flex items-start justify-between gap-2 border-b border-line-soft py-2 last:border-0"
              >
                <div class="min-w-0">
                  <p class="flex flex-wrap items-center gap-1.5">
                    <span class="truncate text-xs text-base-content">
                      {{ secret.description || t('oauthApps.metadataOnly') }}
                    </span>
                    <span class="chip">{{ secret.is_oidc ? 'oidc' : 'api key' }}</span>
                  </p>
                  <p class="mt-0.5 text-2xs text-faint">
                    <span :title="secret.id">{{ shortId(secret.id) }}</span> ·
                    {{ relative(secret.created_at) }} ·
                    {{ remaining(secret.expired_at) }}
                  </p>
                </div>
                <div class="flex shrink-0 items-center gap-1">
                  <IconButton :label="t('oauthApps.rotate')" :disabled="busy" @click="rotateSecret(secret)">
                    <Renew16 aria-hidden="true" />
                  </IconButton>
                  <IconButton
                    :label="t('oauthApps.revoke')"
                    tone="danger"
                    :disabled="busy"
                    @click="pendingSecret = secret"
                  >
                    <TrashCan16 aria-hidden="true" />
                  </IconButton>
                </div>
              </li>
            </ul>
          </ConsolePanel>

          <ConsolePanel :title="t('oauthApps.danger')" :note="t('oauthApps.requireSudo')">
            <p class="voice mb-3 text-[12px]">{{ t('oauthApps.dangerHint') }}</p>
            <button type="button" class="btn btn-error btn-sm" :disabled="busy" @click="confirmDelete = true">
              {{ t('oauthApps.deleteAction') }}
            </button>
          </ConsolePanel>
        </div>
      </div>
    </div>

    <ConfirmAction
      v-if="pendingSecret"
      :title="t('oauthApps.revokeTitle')"
      :body="t('oauthApps.revokeBody')"
      :action="t('oauthApps.revokeAction')"
      tone="error"
      :busy="busy"
      @cancel="pendingSecret = null"
      @confirm="revokeSecret"
    />

    <ConfirmAction
      v-if="confirmDelete"
      :title="t('oauthApps.deleteTitle')"
      :body="t('oauthApps.deleteBody')"
      :action="t('oauthApps.deleteAction')"
      tone="error"
      :busy="busy"
      @cancel="confirmDelete = false"
      @confirm="remove"
    />
  </div>
</template>