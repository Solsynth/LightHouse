<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { useNoticeStore } from '@/stores/notice'
import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type {
  Account,
  AdminBadge,
  AdminBoardItem,
  Contact,
  MagicSpell,
} from '@/lib/api/types'
import { contactTypeLabel } from '@/lib/enums'
import { relative, shortId, utcStamp } from '@/lib/format'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import TextField from '@/components/TextField.vue'

const props = defineProps<{ name: string; account: Account }>()
const { t } = useI18n()
const notice = useNoticeStore()

const contacts = ref<Contact[]>([])
const badges = ref<AdminBadge[]>([])
const board = ref<AdminBoardItem[]>([])
const spells = ref<MagicSpell[]>([])
const loading = ref(true)
const busy = ref(false)
const error = ref('')

const profile = ref({
  first_name: '',
  middle_name: '',
  last_name: '',
  bio: '',
  gender: '',
  pronouns: '',
  time_zone: '',
  location: '',
})

const contactDraft = ref({ type: 0, content: '' })
const badgeDraft = ref({ type: '', label: '', caption: '' })
const spellDraft = ref({ type: 3, code: '', send_email: true })
const verification = ref({
  type: 1,
  title: '',
  description: '',
  verified_by: '',
})
const verified = ref(Boolean(props.account.profile?.verification))

const boardDraft = ref('')
const boardOpen = ref(false)
const payloadItem = ref<AdminBoardItem | null>(null)
const payloadDraft = ref('')
const pending = ref<{
  kind: 'contact' | 'badge' | 'board' | 'spell'
  id: string
  label: string
} | null>(null)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [contactList, badgeList, boardList, spellList] = await Promise.all([
      adminApi.accountContacts(props.name),
      adminApi.badges(props.name),
      adminApi.board(props.name),
      adminApi.accountSpells(props.name),
    ])
    contacts.value = contactList
    badges.value = badgeList
    board.value = boardList
    spells.value = spellList
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('admin.account.identity.errorLoad')
  } finally {
    loading.value = false
  }
}

function seedProfile() {
  const value = props.account.profile
  profile.value = {
    first_name: value?.first_name ?? '',
    middle_name: value?.middle_name ?? '',
    last_name: value?.last_name ?? '',
    bio: value?.bio ?? '',
    gender: value?.gender ?? '',
    pronouns: value?.pronouns ?? '',
    time_zone: value?.time_zone ?? '',
    location: value?.location ?? '',
  }
}

async function saveProfile() {
  busy.value = true
  try {
    await adminApi.updateAccountProfile(props.name, {
      first_name: profile.value.first_name,
      middle_name: profile.value.middle_name,
      last_name: profile.value.last_name,
      bio: profile.value.bio,
      gender: profile.value.gender,
      pronouns: profile.value.pronouns,
      time_zone: profile.value.time_zone,
      location: profile.value.location,
    })
    notice.ok(t('admin.account.identity.noticeProfile'))
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function addContact() {
  busy.value = true
  try {
    await adminApi.createContact(props.name, {
      type: contactDraft.value.type,
      content: contactDraft.value.content.trim(),
    })
    notice.ok(t('admin.account.identity.noticeContact'))
    contactDraft.value = { type: 0, content: '' }
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function contactAction(
  contact: Contact,
  action: 'verify' | 'unverify' | 'primary' | 'public' | 'private',
) {
  busy.value = true
  try {
    if (action === 'verify') await adminApi.verifyContact(props.name, contact.id)
    else if (action === 'unverify') await adminApi.unverifyContact(props.name, contact.id)
    else if (action === 'primary') await adminApi.setPrimaryContact(props.name, contact.id)
    else if (action === 'public')
      await adminApi.setContactVisibility(props.name, contact.id, true)
    else await adminApi.setContactVisibility(props.name, contact.id, false)
    notice.ok(t('admin.account.identity.noticeContact'))
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function verifyAllContacts() {
  const ids = contacts.value.filter((contact) => !contact.verified_at).map((contact) => contact.id)
  if (!ids.length) return
  busy.value = true
  try {
    await adminApi.batchVerifyContacts(props.name, ids)
    notice.ok(t('admin.account.identity.noticeVerifyAll', { n: ids.length }))
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function invalidateCredits() {
  busy.value = true
  try {
    await adminApi.invalidateCredits(props.name)
    notice.ok(t('admin.account.identity.noticeCredits'))
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function grantBadge() {
  busy.value = true
  try {
    await adminApi.grantBadge(props.name, {
      type: badgeDraft.value.type.trim(),
      label: badgeDraft.value.label.trim() || undefined,
      caption: badgeDraft.value.caption.trim() || undefined,
    })
    notice.ok(t('admin.account.identity.noticeBadge'))
    badgeDraft.value = { type: '', label: '', caption: '' }
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function activateBadge(badge: AdminBadge) {
  busy.value = true
  try {
    await adminApi.activateBadge(props.name, badge.id)
    notice.ok(t('admin.account.identity.noticeBadge'))
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

function openBoardEditor() {
  boardDraft.value = JSON.stringify(board.value, null, 2)
  boardOpen.value = true
}

async function saveBoard() {
  busy.value = true
  try {
    const items = JSON.parse(boardDraft.value) as Array<Record<string, unknown>>
    await adminApi.replaceBoard(props.name, items)
    notice.ok(t('admin.account.identity.noticeBoard'))
    boardOpen.value = false
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

function openPayload(item: AdminBoardItem) {
  payloadItem.value = item
  payloadDraft.value = JSON.stringify(item.payload ?? {}, null, 2)
}

async function savePayload() {
  const item = payloadItem.value
  if (!item) return
  busy.value = true
  try {
    await adminApi.updateBoardPayload(
      props.name,
      item.id,
      JSON.parse(payloadDraft.value) as Record<string, unknown>,
    )
    notice.ok(t('admin.account.identity.noticeBoard'))
    payloadItem.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function createSpell() {
  busy.value = true
  try {
    await adminApi.createSpell(props.name, {
      type: spellDraft.value.type,
      code: spellDraft.value.code.trim() || undefined,
      send_email: spellDraft.value.send_email,
    })
    notice.ok(t('admin.account.identity.noticeSpell'))
    spellDraft.value = { type: 3, code: '', send_email: true }
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function resendSpell(spell: MagicSpell) {
  busy.value = true
  try {
    await adminApi.resendSpell(props.name, spell.id, true)
    notice.ok(t('admin.account.identity.noticeSpell'))
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function saveVerification() {
  busy.value = true
  try {
    await adminApi.setVerification(props.name, {
      type: verification.value.type,
      title: verification.value.title || undefined,
      description: verification.value.description || undefined,
      verified_by: verification.value.verified_by || undefined,
    })
    verified.value = true
    notice.ok(t('admin.account.identity.noticeVerification'))
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function clearVerification() {
  busy.value = true
  try {
    await adminApi.clearVerification(props.name)
    verified.value = false
    notice.ok(t('admin.account.identity.noticeVerification'))
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function confirmPending() {
  const target = pending.value
  if (!target) return
  busy.value = true
  try {
    if (target.kind === 'contact') await adminApi.deleteContact(props.name, target.id)
    else if (target.kind === 'badge') await adminApi.revokeBadge(props.name, target.id)
    else if (target.kind === 'board') await adminApi.deleteBoardItem(props.name, target.id)
    else await adminApi.deleteSpell(props.name, target.id)
    notice.ok(t('admin.account.identity.noticeRemoved'))
    pending.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.account.actionFailed'))
  } finally {
    busy.value = false
  }
}

const confirmCopy = computed(() => {
  const target = pending.value
  if (!target) return { title: '', body: '' }
  const labels = {
    contact: t('admin.account.identity.confirmContact'),
    badge: t('admin.account.identity.confirmBadge'),
    board: t('admin.account.identity.confirmBoard'),
    spell: t('admin.account.identity.confirmSpell'),
  }
  return { title: labels[target.kind], body: t('admin.account.identity.confirmBody', { label: target.label }) }
})

onMounted(() => {
  seedProfile()
  void load()
})
</script>

<template>
  <div class="space-y-6">
    <p v-if="error" class="note note-error mb-4">{{ error }}</p>

    <LoadingSkeleton v-if="loading" :rows="4" :columns="[3, 2, 1]" header />

    <template v-else>
      <ConsolePanel :title="t('admin.account.identity.profileTitle')">
        <form class="grid gap-4 sm:grid-cols-2" @submit.prevent="saveProfile">
          <TextField id="profile-first" v-model="profile.first_name" :label="t('admin.account.identity.firstName')" />
          <TextField id="profile-middle" v-model="profile.middle_name" :label="t('admin.account.identity.middleName')" />
          <TextField id="profile-last" v-model="profile.last_name" :label="t('admin.account.identity.lastName')" />
          <TextField id="profile-pronouns" v-model="profile.pronouns" :label="t('admin.account.identity.pronouns')" />
          <TextField id="profile-gender" v-model="profile.gender" :label="t('admin.account.identity.gender')" />
          <TextField id="profile-timezone" v-model="profile.time_zone" :label="t('admin.account.identity.timeZone')" />
          <TextField id="profile-location" v-model="profile.location" :label="t('admin.account.identity.location')" />
          <TextField
            id="profile-bio"
            v-model="profile.bio"
            root-class="sm:col-span-2"
            as="textarea"
            :rows="3"
            :label="t('admin.account.identity.bio')"
          />
          <div class="sm:col-span-2">
            <button type="submit" class="btn btn-primary btn-sm" :disabled="busy">
              {{ t('common.save') }}
            </button>
          </div>
        </form>
      </ConsolePanel>

      <ConsolePanel :title="t('admin.account.identity.contactsTitle')" :note="`${contacts.length}`">
        <div class="mb-3 flex justify-end">
          <button
            type="button"
            class="btn btn-ghost btn-xs"
            :disabled="busy || !contacts.some((contact) => !contact.verified_at)"
            @click="verifyAllContacts"
          >
            {{ t('admin.account.identity.verifyAll') }}
          </button>
        </div>
        <form class="mb-4 flex flex-wrap items-end gap-2" @submit.prevent="addContact">
          <TextField
            id="contact-type"
            v-model.number="contactDraft.type"
            as="select"
            root-class="w-36"
            :label="t('common.type')"
          >
            <option :value="0">{{ t('admin.account.identity.email') }}</option>
            <option :value="1">{{ t('admin.account.identity.phone') }}</option>
            <option :value="2">{{ t('admin.account.identity.address') }}</option>
          </TextField>
          <TextField
            id="contact-content"
            v-model="contactDraft.content"
            root-class="min-w-[200px] flex-1"
            :label="t('admin.account.identity.contactContent')"
          />
          <button
            type="submit"
            class="btn btn-ghost btn-field"
            :disabled="busy || !contactDraft.content.trim()"
          >
            {{ t('common.add') }}
          </button>
        </form>

        <p v-if="!contacts.length" class="voice py-3 text-[12px]">
          {{ t('admin.account.identity.noContacts') }}
        </p>
        <ul v-else>
          <li
            v-for="contact in contacts"
            :key="contact.id"
            class="flex flex-wrap items-center justify-between gap-2 py-2 first:pt-0 last:pb-0"
          >
            <div class="min-w-0">
              <p class="break-all text-xs text-base-content">{{ contact.content }}</p>
              <div class="mt-1 flex flex-wrap gap-1">
                <span class="chip">{{ contactTypeLabel(contact.type) }}</span>
                <span v-if="contact.is_primary" class="chip text-primary">
                  {{ t('admin.account.identity.primary') }}
                </span>
                <span v-if="contact.is_public" class="chip">{{ t('admin.account.identity.public') }}</span>
                <span class="chip" :class="contact.verified_at ? 'text-success' : 'text-warning'">
                  {{ contact.verified_at ? t('admin.account.identity.verified') : t('admin.account.identity.unverified') }}
                </span>
              </div>
            </div>
            <div class="flex flex-wrap items-center gap-1">
              <button
                v-if="!contact.verified_at"
                type="button"
                class="btn btn-ghost btn-xs"
                :disabled="busy"
                @click="contactAction(contact, 'verify')"
              >
                {{ t('admin.account.identity.verify') }}
              </button>
              <button
                v-else
                type="button"
                class="btn btn-ghost btn-xs"
                :disabled="busy"
                @click="contactAction(contact, 'unverify')"
              >
                {{ t('admin.account.identity.unverify') }}
              </button>
              <button
                v-if="!contact.is_primary"
                type="button"
                class="btn btn-ghost btn-xs"
                :disabled="busy"
                @click="contactAction(contact, 'primary')"
              >
                {{ t('admin.account.identity.makePrimary') }}
              </button>
              <button
                type="button"
                class="btn btn-ghost btn-xs"
                :disabled="busy"
                @click="contactAction(contact, contact.is_public ? 'private' : 'public')"
              >
                {{ contact.is_public ? t('admin.account.identity.makePrivate') : t('admin.account.identity.makePublic') }}
              </button>
              <button
                type="button"
                class="btn btn-ghost btn-xs text-error"
                :disabled="busy"
                @click="pending = { kind: 'contact', id: contact.id, label: contact.content }"
              >
                {{ t('admin.account.identity.delete') }}
              </button>
            </div>
          </li>
        </ul>
      </ConsolePanel>

      <ConsolePanel :title="t('admin.account.identity.verificationTitle')">
        <p v-if="verified" class="mb-3 text-2xs text-success">
          {{ t('admin.account.identity.currentlyVerified') }}
        </p>
        <form class="grid gap-4 sm:grid-cols-2" @submit.prevent="saveVerification">
          <TextField
            id="verification-type"
            v-model.number="verification.type"
            type="number"
            :label="t('admin.account.identity.verificationType')"
          />
          <TextField id="verification-title" v-model="verification.title" :label="t('admin.account.identity.verificationTitleField')" />
          <TextField
            id="verification-description"
            v-model="verification.description"
            root-class="sm:col-span-2"
            as="textarea"
            :rows="2"
            :label="t('admin.account.identity.verificationDescription')"
          />
          <TextField id="verification-by" v-model="verification.verified_by" :label="t('admin.account.identity.verifiedBy')" />
          <div class="flex items-end gap-2 sm:col-span-2">
            <button type="submit" class="btn btn-primary btn-sm" :disabled="busy">
              {{ t('admin.account.identity.applyVerification') }}
            </button>
            <button
              v-if="verified"
              type="button"
              class="btn btn-ghost btn-sm"
              :disabled="busy"
              @click="clearVerification"
            >
              {{ t('admin.account.identity.clearVerification') }}
            </button>
          </div>
        </form>
      </ConsolePanel>

      <ConsolePanel :title="t('admin.account.identity.creditsTitle')">
        <p class="voice text-[12px] text-faint">{{ t('admin.account.identity.creditsHint') }}</p>
        <button
          type="button"
          class="btn btn-ghost btn-xs mt-3"
          :disabled="busy"
          @click="invalidateCredits"
        >
          {{ t('admin.account.identity.invalidateCredits') }}
        </button>
      </ConsolePanel>

      <ConsolePanel :title="t('admin.account.identity.badgesTitle')" :note="`${badges.length}`">
        <form class="mb-4 flex flex-wrap items-end gap-2" @submit.prevent="grantBadge">
          <TextField id="badge-type" v-model="badgeDraft.type" root-class="w-40" :label="t('admin.account.identity.badgeType')" />
          <TextField id="badge-label" v-model="badgeDraft.label" root-class="w-40" :label="t('admin.account.identity.badgeLabel')" />
          <TextField id="badge-caption" v-model="badgeDraft.caption" root-class="min-w-[180px] flex-1" :label="t('admin.account.identity.badgeCaption')" />
          <button type="submit" class="btn btn-ghost btn-field" :disabled="busy || !badgeDraft.type.trim()">
            {{ t('admin.account.identity.grantBadge') }}
          </button>
        </form>

        <p v-if="!badges.length" class="voice py-3 text-[12px]">
          {{ t('admin.account.identity.noBadges') }}
        </p>
        <ul v-else class="flex flex-wrap gap-2">
          <li v-for="badge in badges" :key="badge.id" class="flex items-center gap-1 rounded-card bg-base-200/40 p-2">
            <div class="min-w-0">
              <p class="text-xs text-base-content">{{ badge.label || badge.type }}</p>
              <p class="text-2xs text-faint">
                {{ badge.activated_at ? t('admin.account.identity.activeBadge') : t('admin.account.identity.inactiveBadge') }}
              </p>
            </div>
            <button
              v-if="!badge.activated_at"
              type="button"
              class="btn btn-ghost btn-xs"
              :disabled="busy"
              @click="activateBadge(badge)"
            >
              {{ t('admin.account.identity.activate') }}
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-xs text-error"
              :disabled="busy"
              @click="pending = { kind: 'badge', id: badge.id, label: badge.label || badge.type }"
            >
              {{ t('admin.account.identity.delete') }}
            </button>
          </li>
        </ul>
      </ConsolePanel>

      <ConsolePanel :title="t('admin.account.identity.boardTitle')" :note="`${board.length}`">
        <div class="mb-3 flex justify-end">
          <button type="button" class="btn btn-ghost btn-xs" :disabled="busy" @click="openBoardEditor">
            {{ t('admin.account.identity.editBoard') }}
          </button>
        </div>
        <p v-if="!board.length" class="voice py-3 text-[12px]">
          {{ t('admin.account.identity.noBoard') }}
        </p>
        <ul v-else>
          <li
            v-for="item in board"
            :key="item.id"
            class="flex flex-wrap items-center justify-between gap-2 py-2 first:pt-0 last:pb-0"
          >
            <div>
              <p class="text-xs text-base-content">
                {{ item.widget_key || item.custom_app_widget_key || `#${item.kind}` }}
                <span class="chip ml-1">{{ t('admin.account.identity.order', { n: item.order }) }}</span>
                <span class="chip" :class="item.is_enabled ? 'text-success' : 'text-warning'">
                  {{ item.is_enabled ? t('common.enabled') : t('common.disabled') }}
                </span>
              </p>
              <p class="mt-0.5 font-mono text-2xs text-faint">{{ shortId(item.id) }}</p>
            </div>
            <div class="flex items-center gap-1">
              <button type="button" class="btn btn-ghost btn-xs" :disabled="busy" @click="openPayload(item)">
                {{ t('admin.account.identity.payload') }}
              </button>
              <button
                type="button"
                class="btn btn-ghost btn-xs text-error"
                :disabled="busy"
                @click="pending = { kind: 'board', id: item.id, label: item.widget_key || shortId(item.id) }"
              >
                {{ t('admin.account.identity.delete') }}
              </button>
            </div>
          </li>
        </ul>
      </ConsolePanel>

      <ConsolePanel :title="t('admin.account.identity.spellsTitle')" :note="`${spells.length}`">
        <form class="mb-4 flex flex-wrap items-end gap-2" @submit.prevent="createSpell">
          <TextField id="spell-type" v-model.number="spellDraft.type" type="number" root-class="w-28" :label="t('common.type')" />
          <TextField id="spell-code" v-model="spellDraft.code" root-class="min-w-[180px] flex-1" :label="t('admin.account.identity.spellCode')" />
          <label class="flex h-12 items-center gap-2 text-xs text-dim">
            <input v-model="spellDraft.send_email" class="checkbox checkbox-xs" type="checkbox" />
            {{ t('admin.account.identity.sendEmail') }}
          </label>
          <button type="submit" class="btn btn-ghost btn-field" :disabled="busy">
            {{ t('common.add') }}
          </button>
        </form>

        <p v-if="!spells.length" class="voice py-3 text-[12px]">
          {{ t('admin.account.identity.noSpells') }}
        </p>
        <ul v-else>
          <li
            v-for="spell in spells"
            :key="spell.id"
            class="flex flex-wrap items-center justify-between gap-2 py-2 first:pt-0 last:pb-0"
          >
            <div>
              <p class="text-xs text-base-content">{{ t('admin.account.identity.spellType', { n: spell.type }) }}</p>
              <p class="text-2xs text-faint">
                {{ relative(spell.created_at) }} · {{ spell.expires_at ? utcStamp(spell.expires_at) : '—' }}
              </p>
            </div>
            <div class="flex items-center gap-1">
              <button type="button" class="btn btn-ghost btn-xs" :disabled="busy" @click="resendSpell(spell)">
                {{ t('admin.account.identity.resend') }}
              </button>
              <button
                type="button"
                class="btn btn-ghost btn-xs text-error"
                :disabled="busy"
                @click="pending = { kind: 'spell', id: spell.id, label: String(spell.type) }"
              >
                {{ t('admin.account.identity.delete') }}
              </button>
            </div>
          </li>
        </ul>
      </ConsolePanel>
    </template>

    <dialog v-if="boardOpen" class="modal modal-open" aria-modal="true" aria-labelledby="board-title">
      <div class="modal-box max-w-2xl overflow-hidden p-0">
        <div class="bg-base-200/40 px-6 py-5">
          <h2 id="board-title" class="text-sm text-base-content">
            {{ t('admin.account.identity.editBoard') }}
          </h2>
          <p class="voice mt-1 text-[12px]">{{ t('admin.account.identity.boardHint') }}</p>
        </div>
        <form class="px-6 py-5" @submit.prevent="saveBoard">
          <TextField id="board-json" v-model="boardDraft" as="textarea" :rows="14" :label="t('admin.account.identity.boardJson')" />
          <div class="mt-5 flex items-center justify-end gap-2">
            <button type="button" class="btn btn-ghost btn-xs" @click="boardOpen = false">
              {{ t('common.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary btn-xs" :disabled="busy">
              {{ t('common.save') }}
            </button>
          </div>
        </form>
      </div>
      <button type="button" class="modal-backdrop bg-black/60" :aria-label="t('common.cancel')" @click="boardOpen = false" />
    </dialog>

    <dialog v-if="payloadItem" class="modal modal-open" aria-modal="true" aria-labelledby="payload-title">
      <div class="modal-box max-w-xl overflow-hidden p-0">
        <div class="bg-base-200/40 px-6 py-5">
          <h2 id="payload-title" class="text-sm text-base-content">
            {{ t('admin.account.identity.payload') }}
          </h2>
        </div>
        <form class="px-6 py-5" @submit.prevent="savePayload">
          <TextField id="payload-json" v-model="payloadDraft" as="textarea" :rows="12" :label="t('admin.account.identity.boardJson')" />
          <div class="mt-5 flex items-center justify-end gap-2">
            <button type="button" class="btn btn-ghost btn-xs" @click="payloadItem = null">
              {{ t('common.cancel') }}
            </button>
            <button type="submit" class="btn btn-primary btn-xs" :disabled="busy">
              {{ t('common.save') }}
            </button>
          </div>
        </form>
      </div>
      <button type="button" class="modal-backdrop bg-black/60" :aria-label="t('common.cancel')" @click="payloadItem = null" />
    </dialog>

    <ConfirmAction
      v-if="pending"
      :title="confirmCopy.title"
      :body="confirmCopy.body"
      :action="t('admin.account.identity.delete')"
      tone="error"
      :busy="busy"
      @cancel="pending = null"
      @confirm="confirmPending"
    />
  </div>
</template>
