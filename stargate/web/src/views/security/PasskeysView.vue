<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Edit16, TrashCan16 } from '@carbon/icons-vue'
import { RouterLink } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { securityApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AuthFactor, Passkey } from '@/lib/api/types'
import { deviceIdentity } from '@/lib/device'
import { relative, shortId } from '@/lib/format'
import { createCredential, passkeyFailure, passkeysSupported } from '@/lib/webauthn'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import IconButton from '@/components/IconButton.vue'
import SecurityNav from '@/components/SecurityNav.vue'

const { t } = useI18n()
const notice = useNoticeStore()

const passkeys = ref<Passkey[]>([])
const factors = ref<AuthFactor[]>([])
const loading = ref(true)
const error = ref('')
const adding = ref(false)
const busy = ref(false)
const pendingDelete = ref<Passkey | null>(null)
const renaming = ref<Passkey | null>(null)
const renameLabel = ref('')

const supported = passkeysSupported()
const factorEnabled = computed(() =>
  factors.value.some((factor) => factor.type === 7 && Boolean(factor.enabled_at)),
)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [list, configured] = await Promise.all([
      securityApi.passkeys(),
      securityApi.factors(),
    ])
    passkeys.value = list ?? []
    factors.value = configured ?? []
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('passkeys.errorLoad')
  } finally {
    loading.value = false
  }
}

async function addPasskey() {
  if (!supported || !factorEnabled.value) return
  adding.value = true
  error.value = ''
  try {
    const device = deviceIdentity()
    const start = await securityApi.startPasskeyRegistration({
      device_id: device.id,
      device_name: device.name,
    })
    const parts = await createCredential({
      challenge: start.challenge,
      rpId: start.rp_id,
      rpName: start.rp_name,
      userId: start.user_id,
      userName: start.user_name,
      displayName: start.display_name,
      timeout: start.timeout,
      algorithms: start.pub_key_cred_params.map((parameter) => parameter.alg),
    })
    await securityApi.completePasskeyRegistration({
      device_id: device.id,
      client_data_json: parts.client_data_json,
      attestation_object: parts.attestation_object,
      label: device.name,
    })
    notice.ok(t('passkeys.noticeAdded'))
    await load()
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : passkeyFailure(caught)
  } finally {
    adding.value = false
  }
}

function openRename(passkey: Passkey) {
  renaming.value = passkey
  renameLabel.value = passkey.label
}

async function saveRename() {
  const target = renaming.value
  const label = renameLabel.value.trim()
  if (!target || !label) return
  busy.value = true
  try {
    await securityApi.updatePasskey(target.id, label)
    notice.ok(t('passkeys.noticeRenamed'))
    renaming.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('passkeys.errorRename'))
  } finally {
    busy.value = false
  }
}

async function remove() {
  const target = pendingDelete.value
  if (!target) return
  busy.value = true
  try {
    await securityApi.deletePasskey(target.id)
    notice.ok(t('passkeys.noticeDeleted'))
    pendingDelete.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('passkeys.errorDelete'))
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader :title="t('passkeys.title')" :description="t('passkeys.description')">
      <template #actions>
        <button
          type="button"
          class="btn btn-primary btn-sm"
          :disabled="adding || !supported || !factorEnabled"
          @click="addPasskey"
        >
          {{ adding ? t('passkeys.adding') : t('passkeys.add') }}
        </button>
      </template>
    </PageHeader>

    <SecurityNav />

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <div v-if="!supported" class="mb-5 border-l border-warning pl-3 text-xs text-warning">
      {{ t('passkeys.unsupported') }}
    </div>
    <div v-else-if="!factorEnabled" class="mb-5 border-l border-warning pl-3 text-xs text-warning">
      <p>{{ t('passkeys.factorDisabled') }}</p>
      <RouterLink to="/security/factors" class="link-quiet mt-1 inline-block">
        {{ t('passkeys.enableFactor') }}
      </RouterLink>
    </div>

    <ConsolePanel
      :title="t('passkeys.panelTitle')"
      :note="loading ? undefined : t('passkeys.note', { n: passkeys.length })"
    >
      <LoadingSkeleton v-if="loading" variant="rows" :rows="3" />
      <p v-else-if="!passkeys.length" class="voice py-4 text-[12px]">
        {{ t('passkeys.empty') }}
      </p>

      <ul v-else>
        <li
          v-for="passkey in passkeys"
          :key="passkey.id"
          class="flex flex-wrap items-center justify-between gap-3 py-3 first:pt-0 last:pb-0"
        >
          <div class="min-w-0">
            <p class="text-xs text-base-content">
              {{ passkey.label || t('passkeys.unnamed', { id: shortId(passkey.id) }) }}
            </p>
            <p class="mt-1 text-2xs text-faint">
              {{ t('passkeys.created', { time: relative(passkey.created_at) }) }}
            </p>
          </div>
          <div class="flex items-center gap-1">
            <IconButton :label="t('passkeys.rename')" :disabled="busy" @click="openRename(passkey)">
              <Edit16 aria-hidden="true" />
            </IconButton>
            <IconButton
              :label="t('common.delete')"
              tone="danger"
              :disabled="busy"
              @click="pendingDelete = passkey"
            >
              <TrashCan16 aria-hidden="true" />
            </IconButton>
          </div>
        </li>
      </ul>
    </ConsolePanel>

    <ConfirmAction
      v-if="pendingDelete"
      :title="t('passkeys.deleteTitle')"
      :body="
        t('passkeys.deleteBody', {
          label: pendingDelete.label || t('passkeys.unnamed', { id: shortId(pendingDelete.id) }),
        })
      "
      :action="t('passkeys.deleteAction')"
      tone="error"
      :busy="busy"
      @cancel="pendingDelete = null"
      @confirm="remove"
    />

    <dialog
      v-if="renaming"
      class="modal modal-open"
      aria-modal="true"
      aria-labelledby="rename-passkey-title"
    >
      <div class="modal-box max-w-sm rounded-[2px] border border-line bg-base-100 p-0 shadow-none">
        <div class="border-b border-line px-5 py-4">
          <h2 id="rename-passkey-title" class="text-sm text-base-content">
            {{ t('passkeys.renameTitle') }}
          </h2>
        </div>
        <div class="px-5 py-4">
          <label for="passkey-label" class="field-label">{{ t('passkeys.label') }}</label>
          <input
            id="passkey-label"
            v-model="renameLabel"
            class="input input-sm"
            autocomplete="off"
            :disabled="busy"
            @keydown.enter="saveRename"
          />
        </div>
        <div class="flex items-center justify-end gap-2 border-t border-line px-5 py-3">
          <button
            type="button"
            class="btn btn-ghost btn-xs"
            :disabled="busy"
            @click="renaming = null"
          >
            {{ t('common.cancel') }}
          </button>
          <button
            type="button"
            class="btn btn-primary btn-xs"
            :disabled="busy || !renameLabel.trim()"
            @click="saveRename"
          >
            {{ t('common.save') }}
          </button>
        </div>
      </div>
      <button
        type="button"
        class="modal-backdrop bg-black/60"
        :aria-label="t('common.cancel')"
        @click="renaming = null"
      />
    </dialog>
  </div>
</template>
