<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { WarningAlt16 } from '@carbon/icons-vue'
import { useRouter } from 'vue-router'

import { useSessionStore } from '@/stores/session'
import { useNoticeStore } from '@/stores/notice'
import { accountApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { Profile } from '@/lib/api/types'
import { age, utcStamp } from '@/lib/format'
import { installMediaElements } from '@/lib/media'
import { pickImage } from '@/lib/picker'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'

// Register the shared element library before the first render.
installMediaElements()

const { t } = useI18n()
const session = useSessionStore()
const notice = useNoticeStore()

const form = ref({
  first_name: '',
  middle_name: '',
  last_name: '',
  pronouns: '',
  location: '',
  time_zone: '',
  bio: '',
})

const saving = ref(false)
const error = ref('')
const editing = ref(false)
const artBusy = ref<'picture' | 'background' | null>(null)
const artError = ref('')
const deleteOpen = ref(false)
const deleteConfirm = ref('')
const deleting = ref(false)
const deleteError = ref('')
const router = useRouter()

const profile = computed<Profile | undefined>(() => session.account?.profile)

const displayName = computed(() => {
  const value = profile.value
  if (!value) return session.account?.name ?? '—'
  const parts = [value.first_name, value.middle_name, value.last_name].filter(Boolean)
  return parts.length ? parts.join(' ') : (session.account?.name ?? '—')
})

const completion = computed(() => {
  const value = profile.value
  if (!value) return 0
  const fields = [
    value.first_name,
    value.last_name,
    value.bio,
    value.location,
    value.pronouns,
    value.birthday,
  ]
  return Math.round((fields.filter(Boolean).length / fields.length) * 100)
})

function loadForm() {
  const value = profile.value
  form.value = {
    first_name: value?.first_name ?? '',
    middle_name: value?.middle_name ?? '',
    last_name: value?.last_name ?? '',
    pronouns: value?.pronouns ?? '',
    location: value?.location ?? '',
    time_zone: value?.time_zone ?? '',
    bio: value?.bio ?? '',
  }
}

async function save() {
  saving.value = true
  error.value = ''
  try {
    await accountApi.updateProfile({
      first_name: form.value.first_name,
      middle_name: form.value.middle_name,
      last_name: form.value.last_name,
      pronouns: form.value.pronouns,
      location: form.value.location,
      time_zone: form.value.time_zone,
      bio: form.value.bio,
    })
    await session.refreshAccount()
    editing.value = false
    notice.ok(t('account.noticeSaved'))
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('account.errorSave')
  } finally {
    saving.value = false
  }
}

function cancel() {
  loadForm()
  editing.value = false
  error.value = ''
}

/**
 * Profile art is a Drive reference, not an upload: the picker hands back a
 * file id and the profile patch stores it. Saving immediately rather than
 * through the form is deliberate — the picture is one visible object, and a
 * deferred id would let the form's Cancel silently discard it.
 */
async function chooseArt(kind: 'picture' | 'background') {
  artBusy.value = kind
  artError.value = ''
  try {
    const file = await pickImage({
      title: t(kind === 'picture' ? 'account.choosePicture' : 'account.chooseBackground'),
      description: t('account.chooseArtHint'),
      height: kind === 'background' ? 600 : 560,
    })
    if (!file) return
    await accountApi.updateProfile(
      kind === 'picture' ? { picture_id: file.id } : { background_id: file.id },
    )
    await session.refreshAccount()
    notice.ok(t(kind === 'picture' ? 'account.pictureUpdated' : 'account.backgroundUpdated'))
  } catch (caught) {
    artError.value = caught instanceof StargateError ? caught.message : t('account.errorArt')
    notice.fail(artError.value)
  } finally {
    artBusy.value = null
  }
}

async function clearArt(kind: 'picture' | 'background') {
  artBusy.value = kind
  artError.value = ''
  try {
    await accountApi.updateProfile(kind === 'picture' ? { picture_id: '' } : { background_id: '' })
    await session.refreshAccount()
    notice.ok(t(kind === 'picture' ? 'account.pictureCleared' : 'account.backgroundCleared'))
  } catch (caught) {
    artError.value = caught instanceof StargateError ? caught.message : t('account.errorArt')
    notice.fail(artError.value)
  } finally {
    artBusy.value = null
  }
}

function openDelete() {
  deleteConfirm.value = ''
  deleteError.value = ''
  deleteOpen.value = true
}

function closeDelete() {
  if (deleting.value) return
  deleteOpen.value = false
}

async function deleteAccount() {
  if (deleteConfirm.value !== session.account?.name) return
  deleting.value = true
  deleteError.value = ''
  try {
    await accountApi.requestDeletion()
    session.clear()
    deleteOpen.value = false
    notice.ok(t('account.deleted'))
    await router.replace('/sign-in')
  } catch (caught) {
    deleteError.value = caught instanceof StargateError ? caught.message : t('account.deleteError')
  } finally {
    deleting.value = false
  }
}

onMounted(loadForm)
</script>

<template>
  <div>
    <PageHeader :title="t('account.title')" :description="t('account.description')">
      <template #actions>
        <button
          v-if="!editing"
          type="button"
          class="btn btn-primary btn-sm"
          @click="editing = true"
        >
          {{ t('account.editProfile') }}
        </button>
      </template>
    </PageHeader>

    <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
      <div class="space-y-6">
        <!-- Profile art is a Drive reference: the picker chooses the file,
             the profile patch stores its id. -->
        <ConsolePanel :title="t('account.art')">
          <div class="grid gap-5 sm:grid-cols-[104px_minmax(0,1fr)]">
            <div class="space-y-3">
              <div
                class="grid h-[104px] w-[104px] place-items-center overflow-hidden rounded-[2px] border border-line bg-base-300"
              >
                <!-- The picture is a Drive file id: sk-media resolves it and
                     draws an image, so this panel and every other art surface
                     share one renderer. -->
                <sk-media
                  v-if="profile?.picture?.id"
                  :file="profile.picture.id"
                  class="block h-full w-full"
                ></sk-media>
                <span v-else class="text-2xs text-faint">{{ t('account.noPicture') }}</span>
              </div>
              <div class="flex flex-wrap gap-1.5">
                <button
                  type="button"
                  class="btn btn-ghost btn-xs"
                  :disabled="artBusy !== null"
                  @click="chooseArt('picture')"
                >
                  {{ artBusy === 'picture' ? t('account.choosing') : t('account.choosePicture') }}
                </button>
                <button
                  v-if="profile?.picture"
                  type="button"
                  class="btn btn-ghost btn-xs text-dim hover:text-error"
                  :disabled="artBusy !== null"
                  @click="clearArt('picture')"
                >
                  {{ t('common.remove') }}
                </button>
              </div>
            </div>

            <div class="space-y-3">
              <div
                class="grid h-[104px] place-items-center overflow-hidden rounded-[2px] border border-line bg-base-300"
              >
                <sk-media
                  v-if="profile?.background?.id"
                  :file="profile.background.id"
                  class="block h-full w-full"
                ></sk-media>
                <span v-else class="text-2xs text-faint">{{ t('account.noBackground') }}</span>
              </div>
              <div class="flex flex-wrap gap-1.5">
                <button
                  type="button"
                  class="btn btn-ghost btn-xs"
                  :disabled="artBusy !== null"
                  @click="chooseArt('background')"
                >
                  {{
                    artBusy === 'background' ? t('account.choosing') : t('account.chooseBackground')
                  }}
                </button>
                <button
                  v-if="profile?.background"
                  type="button"
                  class="btn btn-ghost btn-xs text-dim hover:text-error"
                  :disabled="artBusy !== null"
                  @click="clearArt('background')"
                >
                  {{ t('common.remove') }}
                </button>
              </div>
            </div>
          </div>
          <p class="mt-4 voice text-[12px] text-faint">{{ t('account.artHint') }}</p>
          <p v-if="artError" class="mt-2 border-l border-error pl-3 text-xs text-error">
            {{ artError }}
          </p>
        </ConsolePanel>

        <ConsolePanel
          :title="t('account.identity')"
          :note="t('account.updated', { time: age(profile?.updated_at) })"
        >
          <dl>
            <div class="grid grid-cols-[132px_minmax(0,1fr)] gap-4 py-2.5 first:pt-0">
              <dt class="text-2xs uppercase tracking-[0.12em] text-faint">
                {{ t('account.name') }}
              </dt>
              <dd class="text-xs text-base-content">{{ session.account?.name }}</dd>
            </div>
            <div class="grid grid-cols-[132px_minmax(0,1fr)] gap-4 py-2.5">
              <dt class="text-2xs uppercase tracking-[0.12em] text-faint">
                {{ t('account.displayName') }}
              </dt>
              <dd class="text-xs text-base-content">{{ displayName }}</dd>
            </div>
            <div class="grid grid-cols-[132px_minmax(0,1fr)] gap-4 py-2.5">
              <dt class="text-2xs uppercase tracking-[0.12em] text-faint">
                {{ t('account.accountId') }}
              </dt>
              <dd class="break-all text-xs text-dim">{{ session.account?.id }}</dd>
            </div>
            <div class="grid grid-cols-[132px_minmax(0,1fr)] gap-4 py-2.5">
              <dt class="text-2xs uppercase tracking-[0.12em] text-faint">
                {{ t('account.status') }}
              </dt>
              <dd class="flex flex-wrap items-center gap-2 text-xs">
                <span class="chip">
                  <span
                    class="dot"
                    :class="session.account?.activated_at ? 'bg-success' : 'bg-warning'"
                  />
                  {{
                    session.account?.activated_at
                      ? t('account.activated')
                      : t('account.pendingActivation')
                  }}
                </span>
                <span v-if="session.account?.is_superuser" class="chip text-accent">
                  {{ t('account.platformAdmin') }}
                </span>
              </dd>
            </div>
            <div class="grid grid-cols-[132px_minmax(0,1fr)] gap-4 py-2.5">
              <dt class="text-2xs uppercase tracking-[0.12em] text-faint">
                {{ t('account.locale') }}
              </dt>
              <dd class="text-xs text-dim">
                {{ session.account?.language }} · {{ session.account?.region }}
              </dd>
            </div>
            <div class="grid grid-cols-[132px_minmax(0,1fr)] gap-4 py-2.5">
              <dt class="text-2xs uppercase tracking-[0.12em] text-faint">
                {{ t('account.memberSince') }}
              </dt>
              <dd class="text-xs text-dim">{{ utcStamp(session.account?.created_at) }}</dd>
            </div>
          </dl>
        </ConsolePanel>

        <ConsolePanel :title="t('account.profile')">
          <form v-if="editing" class="space-y-4" @submit.prevent="save">
            <div class="grid gap-4">
              <div>
                <label for="first_name" class="field-label">{{ t('account.firstName') }}</label>
                <input id="first_name" v-model="form.first_name" class="input input-sm" />
              </div>
              <div>
                <label for="middle_name" class="field-label">{{ t('account.middleName') }}</label>
                <input id="middle_name" v-model="form.middle_name" class="input input-sm" />
              </div>
              <div>
                <label for="last_name" class="field-label">{{ t('account.lastName') }}</label>
                <input id="last_name" v-model="form.last_name" class="input input-sm" />
              </div>
            </div>
            <div class="grid gap-4">
              <div>
                <label for="pronouns" class="field-label">{{ t('account.pronouns') }}</label>
                <input id="pronouns" v-model="form.pronouns" class="input input-sm" />
              </div>
              <div>
                <label for="location" class="field-label">{{ t('account.location') }}</label>
                <input id="location" v-model="form.location" class="input input-sm" />
              </div>
              <div>
                <label for="time_zone" class="field-label">{{ t('account.timeZone') }}</label>
                <input
                  id="time_zone"
                  v-model="form.time_zone"
                  class="input input-sm"
                  placeholder="UTC"
                />
              </div>
            </div>
            <div>
              <label for="bio" class="field-label">{{ t('account.bio') }}</label>
              <textarea id="bio" v-model="form.bio" class="textarea textarea-sm" rows="4" />
            </div>

            <p v-if="error" class="border-l border-error pl-3 text-xs text-error">{{ error }}</p>

            <div class="mt-4 flex items-center gap-2">
              <button type="submit" class="btn btn-primary btn-sm" :disabled="saving">
                {{ saving ? t('common.saving') : t('account.saveProfile') }}
              </button>
              <button type="button" class="btn btn-ghost btn-sm" :disabled="saving" @click="cancel">
                {{ t('common.cancel') }}
              </button>
            </div>
          </form>

          <div v-else>
            <p class="max-w-2xl whitespace-pre-line text-xs leading-relaxed text-dim">
              {{ profile?.bio || t('account.noBio') }}
            </p>
            <dl class="mt-5 grid gap-x-6 gap-y-3 sm:grid-cols-2">
              <div class="flex justify-between pb-2 text-xs">
                <dt class="text-faint">{{ t('account.pronouns') }}</dt>
                <dd class="text-dim">{{ profile?.pronouns || '—' }}</dd>
              </div>
              <div class="flex justify-between pb-2 text-xs">
                <dt class="text-faint">{{ t('account.location') }}</dt>
                <dd class="text-dim">{{ profile?.location || '—' }}</dd>
              </div>
              <div class="flex justify-between pb-2 text-xs">
                <dt class="text-faint">{{ t('account.timeZone') }}</dt>
                <dd class="text-dim">{{ profile?.time_zone || '—' }}</dd>
              </div>
              <div class="flex justify-between pb-2 text-xs">
                <dt class="text-faint">{{ t('account.birthday') }}</dt>
                <dd class="text-dim">
                  {{ profile?.birthday ? utcStamp(profile.birthday).slice(0, 10) : '—' }}
                </dd>
              </div>
            </dl>
          </div>
        </ConsolePanel>
      </div>

      <div class="space-y-6">
        <ConsolePanel :title="t('account.completenessTitle')">
          <div class="flex items-baseline gap-2">
            <span class="text-3xl font-light tabular-nums text-base-content">{{ completion }}</span>
            <span class="text-2xs text-faint">{{ t('account.percent') }}</span>
          </div>
          <div class="mt-3 h-px w-full bg-line">
            <div class="h-px bg-primary" :style="{ width: `${completion}%` }" />
          </div>
          <p class="mt-3 voice text-[12px] text-faint">
            {{ t('account.completenessHint') }}
          </p>
        </ConsolePanel>

        <ConsolePanel :title="t('account.standing')">
          <dl class="space-y-3">
            <div class="flex items-baseline justify-between text-xs">
              <dt class="text-faint">{{ t('account.level') }}</dt>
              <dd class="tabular-nums text-dim">{{ profile?.level ?? 0 }}</dd>
            </div>
            <div class="flex items-baseline justify-between text-xs">
              <dt class="text-faint">{{ t('account.experience') }}</dt>
              <dd class="tabular-nums text-dim">{{ profile?.experience ?? 0 }}</dd>
            </div>
            <div class="flex items-baseline justify-between text-xs">
              <dt class="text-faint">{{ t('account.socialCredits') }}</dt>
              <dd class="tabular-nums text-dim">{{ profile?.social_credits ?? 0 }}</dd>
            </div>
            <div class="flex items-baseline justify-between text-xs">
              <dt class="text-faint">{{ t('account.perkLevel') }}</dt>
              <dd class="tabular-nums text-dim">{{ session.account?.perk_level ?? 0 }}</dd>
            </div>
          </dl>
        </ConsolePanel>

        <ConsolePanel :title="t('account.lastSeen')">
          <p class="text-xs text-dim">
            {{ t('account.lastSeenAgo', { time: age(profile?.last_seen_at) }) }}
          </p>
          <p class="mt-1 text-2xs text-faint">
            {{ t('account.lastSeenHint') }}
          </p>
          <p v-if="profile?.last_seen_at" class="mt-3 text-2xs text-faint">
            {{ utcStamp(profile.last_seen_at) }}
          </p>
        </ConsolePanel>

        <ConsolePanel :title="t('account.dangerZone')">
          <p class="voice text-[12px]">{{ t('account.dangerZoneHint') }}</p>
          <button type="button" class="btn btn-error btn-sm mt-4 w-full" @click="openDelete">
            {{ t('account.deleteAccount') }}
          </button>
        </ConsolePanel>
      </div>
    </div>

    <dialog
      v-if="deleteOpen"
      class="modal modal-open"
      aria-modal="true"
      aria-labelledby="delete-account-title"
    >
      <div class="modal-box max-w-md rounded-[2px] border border-line bg-base-100 p-0 shadow-none">
        <div class="border-b border-line px-5 py-4">
          <h2 id="delete-account-title" class="flex items-center gap-1.5 text-sm text-base-content">
            <WarningAlt16 class="shrink-0 text-error" aria-hidden="true" />
            {{ t('account.deleteTitle') }}
          </h2>
        </div>
        <div class="px-5 py-4">
          <p class="voice">{{ t('account.deleteBody') }}</p>
          <label for="delete-confirm" class="field-label mt-4">
            {{ t('account.deleteConfirmLabel', { name: session.account?.name }) }}
          </label>
          <input
            id="delete-confirm"
            v-model="deleteConfirm"
            class="input input-sm"
            autocomplete="off"
            spellcheck="false"
            :disabled="deleting"
          />
          <p v-if="deleteError" class="mt-3 text-xs text-error">{{ deleteError }}</p>
        </div>
        <div class="flex items-center justify-end gap-2 border-t border-line px-5 py-3">
          <button
            type="button"
            class="btn btn-ghost btn-xs"
            :disabled="deleting"
            @click="closeDelete"
          >
            {{ t('common.cancel') }}
          </button>
          <button
            type="button"
            class="btn btn-error btn-xs"
            :disabled="deleting || deleteConfirm !== session.account?.name"
            @click="deleteAccount"
          >
            {{ deleting ? t('common.loading') : t('account.deleteAction') }}
          </button>
        </div>
      </div>
      <button
        type="button"
        class="modal-backdrop bg-black/60"
        :aria-label="t('common.cancel')"
        @click="closeDelete"
      />
    </dialog>
  </div>
</template>
