<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import { useSessionStore } from '@/stores/session'
import { useNoticeStore } from '@/stores/notice'
import { accountApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { Profile } from '@/lib/api/types'
import { age, utcStamp } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'

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
    notice.ok('Profile saved.')
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : 'The profile could not be saved.'
  } finally {
    saving.value = false
  }
}

function cancel() {
  loadForm()
  editing.value = false
  error.value = ''
}

onMounted(loadForm)
</script>

<template>
  <div>
    <PageHeader
      title="Your Account"
      description="The identity every Solar Network service reads. Changes publish to the profile read-model immediately."
    >
      <template #actions>
        <button v-if="!editing" type="button" class="btn btn-primary btn-sm" @click="editing = true">
          Edit Profile
        </button>
      </template>
    </PageHeader>

    <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
      <div class="space-y-6">
        <ConsolePanel title="Identity" :note="`updated ${age(profile?.updated_at)}`">
          <dl class="divide-y divide-line-soft">
            <div class="grid grid-cols-[132px_minmax(0,1fr)] gap-4 py-2.5 first:pt-0">
              <dt class="text-2xs uppercase tracking-[0.12em] text-faint">name</dt>
              <dd class="text-xs text-base-content">{{ session.account?.name }}</dd>
            </div>
            <div class="grid grid-cols-[132px_minmax(0,1fr)] gap-4 py-2.5">
              <dt class="text-2xs uppercase tracking-[0.12em] text-faint">display name</dt>
              <dd class="text-xs text-base-content">{{ displayName }}</dd>
            </div>
            <div class="grid grid-cols-[132px_minmax(0,1fr)] gap-4 py-2.5">
              <dt class="text-2xs uppercase tracking-[0.12em] text-faint">account id</dt>
              <dd class="break-all text-xs text-dim">{{ session.account?.id }}</dd>
            </div>
            <div class="grid grid-cols-[132px_minmax(0,1fr)] gap-4 py-2.5">
              <dt class="text-2xs uppercase tracking-[0.12em] text-faint">status</dt>
              <dd class="flex flex-wrap items-center gap-2 text-xs">
                <span class="chip">
                  <span
                    class="dot"
                    :class="session.account?.activated_at ? 'bg-success' : 'bg-warning'"
                  />
                  {{ session.account?.activated_at ? 'activated' : 'pending activation' }}
                </span>
                <span v-if="session.account?.is_superuser" class="chip text-accent">
                  platform admin
                </span>
              </dd>
            </div>
            <div class="grid grid-cols-[132px_minmax(0,1fr)] gap-4 py-2.5">
              <dt class="text-2xs uppercase tracking-[0.12em] text-faint">locale</dt>
              <dd class="text-xs text-dim">
                {{ session.account?.language }} · {{ session.account?.region }}
              </dd>
            </div>
            <div class="grid grid-cols-[132px_minmax(0,1fr)] gap-4 py-2.5">
              <dt class="text-2xs uppercase tracking-[0.12em] text-faint">member since</dt>
              <dd class="text-xs text-dim">{{ utcStamp(session.account?.created_at) }}</dd>
            </div>
          </dl>
        </ConsolePanel>

        <ConsolePanel title="Profile">
          <form v-if="editing" class="space-y-4" @submit.prevent="save">
            <div class="grid gap-4">
              <div>
                <label for="first_name" class="field-label">First name</label>
                <input id="first_name" v-model="form.first_name" class="input input-sm" />
              </div>
              <div>
                <label for="middle_name" class="field-label">Middle</label>
                <input id="middle_name" v-model="form.middle_name" class="input input-sm" />
              </div>
              <div>
                <label for="last_name" class="field-label">Last name</label>
                <input id="last_name" v-model="form.last_name" class="input input-sm" />
              </div>
            </div>
            <div class="grid gap-4">
              <div>
                <label for="pronouns" class="field-label">Pronouns</label>
                <input id="pronouns" v-model="form.pronouns" class="input input-sm" />
              </div>
              <div>
                <label for="location" class="field-label">Location</label>
                <input id="location" v-model="form.location" class="input input-sm" />
              </div>
              <div>
                <label for="time_zone" class="field-label">Time zone</label>
                <input id="time_zone" v-model="form.time_zone" class="input input-sm" placeholder="UTC" />
              </div>
            </div>
            <div>
              <label for="bio" class="field-label">Bio</label>
              <textarea id="bio" v-model="form.bio" class="textarea textarea-sm" rows="4" />
            </div>

            <p v-if="error" class="border-l border-error pl-3 text-xs text-error">{{ error }}</p>

            <div class="flex items-center gap-2 border-t border-line-soft pt-4">
              <button type="submit" class="btn btn-primary btn-sm" :disabled="saving">
                {{ saving ? 'Saving…' : 'Save profile' }}
              </button>
              <button type="button" class="btn btn-ghost btn-sm" :disabled="saving" @click="cancel">
                Cancel
              </button>
            </div>
          </form>

          <div v-else>
            <p class="max-w-2xl whitespace-pre-line text-xs leading-relaxed text-dim">
              {{ profile?.bio || 'No bio yet. This is what other accounts read on your profile.' }}
            </p>
            <dl class="mt-5 grid gap-x-6 gap-y-3 sm:grid-cols-2">
              <div class="flex justify-between border-b border-line-soft pb-2 text-xs">
                <dt class="text-faint">pronouns</dt>
                <dd class="text-dim">{{ profile?.pronouns || '—' }}</dd>
              </div>
              <div class="flex justify-between border-b border-line-soft pb-2 text-xs">
                <dt class="text-faint">location</dt>
                <dd class="text-dim">{{ profile?.location || '—' }}</dd>
              </div>
              <div class="flex justify-between border-b border-line-soft pb-2 text-xs">
                <dt class="text-faint">time zone</dt>
                <dd class="text-dim">{{ profile?.time_zone || '—' }}</dd>
              </div>
              <div class="flex justify-between border-b border-line-soft pb-2 text-xs">
                <dt class="text-faint">birthday</dt>
                <dd class="text-dim">{{ profile?.birthday ? utcStamp(profile.birthday).slice(0, 10) : '—' }}</dd>
              </div>
            </dl>
          </div>
        </ConsolePanel>
      </div>

      <div class="space-y-6">
        <ConsolePanel title="Profile Completeness">
          <div class="flex items-baseline gap-2">
            <span class="text-3xl font-light tabular-nums text-base-content">{{ completion }}</span>
            <span class="text-2xs text-faint">percent</span>
          </div>
          <div class="mt-3 h-px w-full bg-line">
            <div class="h-px bg-primary" :style="{ width: `${completion}%` }" />
          </div>
          <p class="mt-3 voice text-[12px] text-faint">
            Complete profiles appear in account search and public listings.
          </p>
        </ConsolePanel>

        <ConsolePanel title="Standing">
          <dl class="space-y-3">
            <div class="flex items-baseline justify-between text-xs">
              <dt class="text-faint">level</dt>
              <dd class="tabular-nums text-dim">{{ profile?.level ?? 0 }}</dd>
            </div>
            <div class="flex items-baseline justify-between text-xs">
              <dt class="text-faint">experience</dt>
              <dd class="tabular-nums text-dim">{{ profile?.experience ?? 0 }}</dd>
            </div>
            <div class="flex items-baseline justify-between text-xs">
              <dt class="text-faint">social credits</dt>
              <dd class="tabular-nums text-dim">{{ profile?.social_credits ?? 0 }}</dd>
            </div>
            <div class="flex items-baseline justify-between text-xs">
              <dt class="text-faint">perk level</dt>
              <dd class="tabular-nums text-dim">{{ session.account?.perk_level ?? 0 }}</dd>
            </div>
          </dl>
        </ConsolePanel>

        <ConsolePanel title="Last Seen">
          <p class="text-xs text-dim">{{ age(profile?.last_seen_at) }} ago</p>
          <p class="mt-1 text-2xs text-faint">
            Written by the service on authenticated requests, batched every five seconds.
          </p>
          <p v-if="profile?.last_seen_at" class="mt-3 text-2xs text-faint">
            {{ utcStamp(profile.last_seen_at) }}
          </p>
        </ConsolePanel>
      </div>
    </div>
  </div>
</template>
