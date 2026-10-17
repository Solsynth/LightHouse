<script setup lang="ts">
import { UserAvatar32 } from '@carbon/icons-vue'
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'

import ConsolePanel from '@/components/ConsolePanel.vue'
import PageHeader from '@/components/PageHeader.vue'
import { accountApi, socialApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { Account } from '@/lib/api/types'
import { contactTypeLabel } from '@/lib/enums'
import { utcStamp } from '@/lib/format'

const { t, locale } = useI18n()
const route = useRoute()

const name = computed(() => String(route.params.name ?? ''))
const account = ref<Account | null>(null)
const followers = ref(0)
const following = ref(0)
const loading = ref(true)
const notFound = ref(false)
const avatarFailed = ref(false)
const bannerFailed = ref(false)

const profile = computed(() => account.value?.profile)
const displayName = computed(() => {
  const value = account.value
  if (!value) return ''
  const parts = [value.profile?.first_name, value.profile?.middle_name, value.profile?.last_name]
    .filter(Boolean)
    .join(' ')
  return value.nick || parts || value.name
})
const canonicalName = computed(() => account.value?.name ?? name.value)
const pictureUrl = computed(
  () =>
    profile.value?.picture?.url ||
    `/api/accounts/${encodeURIComponent(canonicalName.value)}/picture`,
)
const backgroundUrl = computed(
  () =>
    profile.value?.background?.url ||
    `/api/accounts/${encodeURIComponent(canonicalName.value)}/background`,
)
const hasPicture = computed(() => Boolean(profile.value?.picture))
const hasBackground = computed(() => Boolean(profile.value?.background))
const publicContacts = computed(() =>
  (account.value?.contacts ?? []).filter((contact) => contact.is_public),
)
const joined = computed(() => profile.value?.created_at ?? account.value?.created_at)

let loadId = 0

function updateTitle() {
  const suffix = 'Solarpass'
  document.title = account.value
    ? `${displayName.value} (@${account.value.name}) · ${suffix}`
    : `${t('publicProfile.notFoundTitle')} · ${suffix}`
}

async function load() {
  const id = ++loadId
  loading.value = true
  notFound.value = false
  avatarFailed.value = false
  bannerFailed.value = false

  try {
    const loaded = await accountApi.byName(name.value)
    if (id !== loadId) return
    account.value = loaded

    // Totals are decoration around the profile; a follow-page failure must not
    // take the profile down with it.
    try {
      const [followersPage, followingPage] = await Promise.all([
        socialApi.followers(loaded.name, { take: 1 }),
        socialApi.following(loaded.name, { take: 1 }),
      ])
      if (id !== loadId) return
      followers.value = followersPage.total
      following.value = followingPage.total
    } catch {
      if (id !== loadId) return
      followers.value = 0
      following.value = 0
    }
  } catch (error) {
    if (id !== loadId) return
    account.value = null
    notFound.value = error instanceof StargateError && error.status === 404
  }

  loading.value = false
  updateTitle()
}

onMounted(load)
watch(name, load)
watch([locale, () => account.value?.name], updateTitle)
</script>

<template>
  <div>
    <div v-if="loading" class="mx-auto w-full max-w-[980px] px-5 py-16 text-center text-xs text-faint">
      {{ t('publicProfile.loading') }}
    </div>

    <template v-else-if="!account">
      <div class="mx-auto w-full max-w-[980px] px-5 py-10">
        <PageHeader
          :title="t('publicProfile.notFoundTitle')"
          :description="t(notFound ? 'publicProfile.notFoundBody' : 'publicProfile.errorLoad')"
        />
        <RouterLink to="/" class="link-quiet text-xs">
          {{ t('publicProfile.goHome') }}
        </RouterLink>
      </div>
    </template>

    <template v-else>
      <section>
        <div class="relative h-48 w-full overflow-hidden bg-base-300/50 sm:h-60">
          <img
            v-if="hasBackground && !bannerFailed"
            :src="backgroundUrl"
            alt=""
            class="h-full w-full object-cover"
            @error="bannerFailed = true"
          />
        </div>

        <div class="mx-auto w-full max-w-[980px] px-5 sm:px-7">
          <div class="flex flex-wrap items-end gap-4">
            <div
              class="-mt-10 h-20 w-20 shrink-0 overflow-hidden rounded-[2px] bg-base-300"
            >
              <img
                v-if="hasPicture && !avatarFailed"
                :src="pictureUrl"
                alt=""
                class="h-full w-full object-cover"
                @error="avatarFailed = true"
              />
              <span v-else class="flex h-full w-full items-center justify-center text-faint">
                <UserAvatar32 aria-hidden="true" />
              </span>
            </div>
            <div class="min-w-0 pb-1">
              <h1 class="title text-xl text-base-content sm:text-2xl">{{ displayName }}</h1>
              <p class="mt-1 text-2xs text-faint">@{{ account.name }}</p>
            </div>
          </div>

          <div class="mt-8 grid gap-6 pb-10 lg:grid-cols-[minmax(0,1fr)_320px]">
            <div class="space-y-6">
              <ConsolePanel :title="t('publicProfile.about')">
                <p class="voice max-w-2xl whitespace-pre-line">
                  {{ profile?.bio || t('publicProfile.noBio') }}
                </p>
              </ConsolePanel>

              <ConsolePanel v-if="profile?.links?.length" :title="t('publicProfile.links')">
                <ul class="space-y-3 text-xs">
                  <li
                    v-for="link in profile.links"
                    :key="link.url"
                    class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1"
                  >
                    <span class="text-faint">{{ link.name }}</span>
                    <a
                      :href="link.url"
                      class="link-quiet max-w-full truncate"
                      target="_blank"
                      rel="noopener noreferrer nofollow"
                    >
                      {{ link.url }}
                    </a>
                  </li>
                </ul>
              </ConsolePanel>

              <ConsolePanel v-if="publicContacts.length" :title="t('publicProfile.contacts')">
                <ul class="space-y-3 text-xs">
                  <li
                    v-for="contact in publicContacts"
                    :key="contact.id"
                    class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1"
                  >
                    <span class="text-faint">{{ contactTypeLabel(contact.type) }}</span>
                    <span class="break-all text-dim">{{ contact.content }}</span>
                  </li>
                </ul>
              </ConsolePanel>
            </div>

            <ConsolePanel :title="t('publicProfile.details')">
              <dl class="space-y-3 text-xs">
                <div class="flex items-baseline justify-between gap-4">
                  <dt class="text-faint">{{ t('publicProfile.followers') }}</dt>
                  <dd class="tabular-nums text-dim">{{ followers }}</dd>
                </div>
                <div class="flex items-baseline justify-between gap-4">
                  <dt class="text-faint">{{ t('publicProfile.following') }}</dt>
                  <dd class="tabular-nums text-dim">{{ following }}</dd>
                </div>
                <div v-if="profile?.pronouns" class="flex items-baseline justify-between gap-4">
                  <dt class="text-faint">{{ t('publicProfile.pronouns') }}</dt>
                  <dd class="text-dim">{{ profile.pronouns }}</dd>
                </div>
                <div v-if="profile?.location" class="flex items-baseline justify-between gap-4">
                  <dt class="text-faint">{{ t('publicProfile.location') }}</dt>
                  <dd class="text-dim">{{ profile.location }}</dd>
                </div>
                <div v-if="profile?.time_zone" class="flex items-baseline justify-between gap-4">
                  <dt class="text-faint">{{ t('publicProfile.timeZone') }}</dt>
                  <dd class="text-dim">{{ profile.time_zone }}</dd>
                </div>
                <div v-if="joined" class="flex items-baseline justify-between gap-4">
                  <dt class="text-faint">{{ t('publicProfile.joined') }}</dt>
                  <dd class="tabular-nums text-dim">{{ utcStamp(joined).slice(0, 10) }}</dd>
                </div>
              </dl>
            </ConsolePanel>
          </div>
        </div>
      </section>
    </template>
  </div>
</template>
