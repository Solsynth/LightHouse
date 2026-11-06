<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute, useRouter } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AdminRealmDetail, RealmMember, VerificationMark } from '@/lib/api/types'
import { relative, shortId } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import AdminNav from '@/components/AdminNav.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import TextField from '@/components/TextField.vue'

const ROLES = [
  { value: 0, key: 'normal' },
  { value: 50, key: 'moderator' },
  { value: 100, key: 'owner' },
] as const

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const notice = useNoticeStore()

const detail = ref<AdminRealmDetail | null>(null)
const members = ref<RealmMember[]>([])
const memberTotal = ref(0)
const loading = ref(true)
const busy = ref(false)
const error = ref('')
const pendingDelete = ref(false)

const slug = computed(() => String(route.params.slug ?? ''))
const form = ref({
  slug: '',
  name: '',
  description: '',
  is_community: false,
  is_public: false,
  account_id: '',
})
const verification = ref<VerificationMark>({ type: 1 })
const hasVerification = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [realmDetail, memberPage] = await Promise.all([
      adminApi.realm(slug.value),
      adminApi.realmMembers(slug.value, { take: 50 }),
    ])
    detail.value = realmDetail
    members.value = memberPage.items
    memberTotal.value = memberPage.total
    form.value = {
      slug: realmDetail.realm.slug,
      name: realmDetail.realm.name,
      description: realmDetail.realm.description,
      is_community: realmDetail.realm.is_community,
      is_public: realmDetail.realm.is_public,
      account_id: realmDetail.realm.account_id,
    }
    hasVerification.value = Boolean(realmDetail.realm.verification)
    verification.value = realmDetail.realm.verification ?? { type: 1 }
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('admin.realm.errorLoad')
  } finally {
    loading.value = false
  }
}

async function saveRealm() {
  busy.value = true
  try {
    await adminApi.updateRealm(slug.value, {
      slug: form.value.slug.trim() || undefined,
      name: form.value.name.trim() || undefined,
      description: form.value.description,
      is_community: form.value.is_community,
      is_public: form.value.is_public,
      account_id: form.value.account_id.trim() || undefined,
    })
    notice.ok(t('admin.realm.noticeSaved'))
    if (form.value.slug.trim() && form.value.slug.trim() !== slug.value) {
      await router.replace(`/admin/realms/${encodeURIComponent(form.value.slug.trim())}`)
    }
    await load()
  } catch (caught) {
    notice.fail(caught instanceof StargateError ? caught.message : t('admin.realm.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function saveVerification() {
  busy.value = true
  try {
    await adminApi.setRealmVerification(slug.value, {
      type: verification.value.type,
      title: verification.value.title || undefined,
      description: verification.value.description || undefined,
      verified_by: verification.value.verified_by || undefined,
    })
    notice.ok(t('admin.realm.noticeVerified'))
    await load()
  } catch (caught) {
    notice.fail(caught instanceof StargateError ? caught.message : t('admin.realm.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function clearVerification() {
  busy.value = true
  try {
    await adminApi.clearRealmVerification(slug.value)
    notice.ok(t('admin.realm.noticeUnverified'))
    await load()
  } catch (caught) {
    notice.fail(caught instanceof StargateError ? caught.message : t('admin.realm.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function setRole(member: RealmMember, role: number) {
  busy.value = true
  try {
    await adminApi.updateRealmMemberRole(slug.value, member.account_id, role)
    notice.ok(t('admin.realm.noticeRole'))
    await load()
  } catch (caught) {
    notice.fail(caught instanceof StargateError ? caught.message : t('admin.realm.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function removeMember(member: RealmMember) {
  busy.value = true
  try {
    await adminApi.removeRealmMember(slug.value, member.account_id)
    notice.ok(t('admin.realm.noticeMemberRemoved'))
    await load()
  } catch (caught) {
    notice.fail(caught instanceof StargateError ? caught.message : t('admin.realm.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function deleteRealm() {
  busy.value = true
  try {
    await adminApi.deleteRealm(slug.value)
    notice.ok(t('admin.realm.noticeDeleted'))
    await router.push('/admin/realms')
  } catch (caught) {
    notice.fail(caught instanceof StargateError ? caught.message : t('admin.realm.actionFailed'))
  } finally {
    busy.value = false
    pendingDelete.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      :title="detail?.realm.name || t('admin.realm.title')"
      :description="detail ? `/${detail.realm.slug}` : t('admin.realm.description')"
    >
      <template #actions>
        <RouterLink to="/admin/realms" class="btn btn-ghost btn-sm">
          {{ t('admin.realm.allRealms') }}
        </RouterLink>
      </template>
    </PageHeader>

    <AdminNav />

    <p v-if="error" class="note note-error mb-4">{{ error }}</p>

    <LoadingSkeleton v-if="loading" compact :rows="6" :columns="[4, 3, 2, 1]" header />

    <div v-else-if="detail" class="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
      <div class="space-y-6">
        <ConsolePanel :title="t('admin.realm.identityTitle')">
          <form class="grid gap-4 sm:grid-cols-2" @submit.prevent="saveRealm">
            <TextField id="realm-slug" v-model="form.slug" :label="t('admin.realm.slug')" />
            <TextField id="realm-name" v-model="form.name" :label="t('admin.realm.name')" />
            <TextField
              id="realm-description"
              v-model="form.description"
              root-class="sm:col-span-2"
              as="textarea"
              :rows="3"
              :label="t('admin.realm.realmDescription')"
            />
            <TextField
              id="realm-owner"
              v-model="form.account_id"
              root-class="sm:col-span-2"
              :label="t('admin.realm.ownerId')"
              :hint="t('admin.realm.ownerHint')"
            />
            <div class="flex flex-wrap gap-4 text-xs text-dim sm:col-span-2">
              <label class="flex items-center gap-2">
                <input v-model="form.is_public" class="checkbox checkbox-xs" type="checkbox" />
                {{ t('admin.realm.public') }}
              </label>
              <label class="flex items-center gap-2">
                <input v-model="form.is_community" class="checkbox checkbox-xs" type="checkbox" />
                {{ t('admin.realm.community') }}
              </label>
            </div>
            <div class="sm:col-span-2">
              <button type="submit" class="btn btn-primary btn-sm" :disabled="busy">
                {{ t('admin.realm.save') }}
              </button>
            </div>
          </form>
        </ConsolePanel>

        <ConsolePanel :title="t('admin.realm.membersTitle')" :note="`${memberTotal}`">
          <p v-if="!members.length" class="voice py-3 text-[12px]">
            {{ t('admin.realm.noMembers') }}
          </p>
          <ul v-else>
            <li
              v-for="member in members"
              :key="member.account_id"
              class="flex flex-wrap items-center justify-between gap-2 py-2 first:pt-0 last:pb-0"
            >
              <div class="min-w-0">
                <RouterLink
                  v-if="member.account"
                  :to="`/admin/accounts/${encodeURIComponent(member.account.name)}`"
                  class="text-xs text-base-content hover:underline"
                >
                  {{ member.account.nick || member.account.name }}
                </RouterLink>
                <span v-else class="font-mono text-2xs text-faint">
                  {{ shortId(member.account_id) }}
                </span>
                <p class="mt-0.5 text-2xs text-faint">
                  {{ t('admin.realm.levelShort', { n: member.level }) }}
                  · {{ t('admin.realm.joined', { time: relative(member.joined_at) }) }}
                </p>
              </div>
              <div class="flex items-center gap-2">
                <select
                  class="select select-xs"
                  :value="member.role"
                  :disabled="busy"
                  @change="
                    setRole(member, Number(($event.target as HTMLSelectElement).value))
                  "
                >
                  <option v-for="role in ROLES" :key="role.value" :value="role.value">
                    {{ t(`admin.realm.roles.${role.key}`) }}
                  </option>
                </select>
                <button
                  type="button"
                  class="btn btn-ghost btn-xs text-error"
                  :disabled="busy"
                  @click="removeMember(member)"
                >
                  {{ t('admin.realm.remove') }}
                </button>
              </div>
            </li>
          </ul>
        </ConsolePanel>
      </div>

      <div class="space-y-6">
        <ConsolePanel :title="t('admin.realm.countsTitle')">
          <dl class="space-y-3 text-xs">
            <div class="flex justify-between">
              <dt class="text-faint">{{ t('admin.realm.memberCount') }}</dt>
              <dd class="tabular-nums text-dim">{{ detail.member_count }}</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-faint">{{ t('admin.realm.pendingInvites') }}</dt>
              <dd class="tabular-nums text-dim">{{ detail.pending_invite_count }}</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-faint">{{ t('admin.realm.labelCount') }}</dt>
              <dd class="tabular-nums text-dim">{{ detail.label_count }}</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-faint">{{ t('admin.realm.activeBoosts') }}</dt>
              <dd class="tabular-nums text-dim">
                {{ detail.active_boost_contribution_count }}
              </dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-faint">{{ t('admin.realm.boostPoints') }}</dt>
              <dd class="tabular-nums text-dim">
                {{ detail.realm.boost_points }} · L{{ detail.realm.boost_level }}
              </dd>
            </div>
          </dl>
        </ConsolePanel>

        <ConsolePanel :title="t('admin.realm.verificationTitle')">
          <p v-if="hasVerification" class="mb-3 text-2xs text-success">
            {{ t('admin.realm.currentlyVerified') }}
          </p>
          <form class="space-y-3" @submit.prevent="saveVerification">
            <TextField
              id="realm-verification-type"
              v-model.number="verification.type"
              type="number"
              :label="t('admin.realm.verificationType')"
            />
            <TextField
              id="realm-verification-title"
              v-model="verification.title"
              :label="t('admin.realm.verificationTitleField')"
            />
            <TextField
              id="realm-verification-description"
              v-model="verification.description"
              as="textarea"
              :rows="2"
              :label="t('admin.realm.verificationDescription')"
            />
            <TextField
              id="realm-verification-by"
              v-model="verification.verified_by"
              :label="t('admin.realm.verifiedBy')"
            />
            <div class="flex gap-2">
              <button type="submit" class="btn btn-primary btn-xs" :disabled="busy">
                {{ t('admin.realm.applyVerification') }}
              </button>
              <button
                v-if="hasVerification"
                type="button"
                class="btn btn-ghost btn-xs"
                :disabled="busy"
                @click="clearVerification"
              >
                {{ t('admin.realm.clearVerification') }}
              </button>
            </div>
          </form>
        </ConsolePanel>

        <ConsolePanel :title="t('admin.realm.dangerTitle')">
          <button
            type="button"
            class="btn btn-error btn-sm w-full"
            :disabled="busy"
            @click="pendingDelete = true"
          >
            {{ t('admin.realm.delete') }}
          </button>
          <p class="voice mt-3 text-[12px] text-faint">{{ t('admin.realm.deleteHint') }}</p>
        </ConsolePanel>
      </div>
    </div>

    <ConfirmAction
      v-if="pendingDelete"
      :title="t('admin.realm.confirmDeleteTitle')"
      :body="t('admin.realm.confirmDeleteBody', { name: detail?.realm.name || slug })"
      :action="t('admin.realm.delete')"
      tone="error"
      :busy="busy"
      @cancel="pendingDelete = false"
      @confirm="deleteRealm"
    />
  </div>
</template>
