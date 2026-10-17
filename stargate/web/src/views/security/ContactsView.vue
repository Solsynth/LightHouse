<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { useNoticeStore } from '@/stores/notice'
import { securityApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { Contact } from '@/lib/api/types'
import { contactTypeLabel } from '@/lib/enums'
import { relative, utcStamp } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import SecurityNav from '@/components/SecurityNav.vue'

const { t } = useI18n()
const notice = useNoticeStore()

const contacts = ref<Contact[]>([])
const loading = ref(true)
const error = ref('')
const busy = ref(false)
const pendingDelete = ref<Contact | null>(null)

const type = ref(0)
const content = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    contacts.value = await securityApi.contacts()
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('contacts.errorLoad')
  } finally {
    loading.value = false
  }
}

async function add() {
  if (!content.value.trim()) return
  busy.value = true
  error.value = ''
  try {
    await securityApi.createContact(type.value, content.value.trim())
    content.value = ''
    notice.ok(t('contacts.noticeAdded'))
    await load()
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('contacts.errorAdd')
  } finally {
    busy.value = false
  }
}

async function verify(contact: Contact) {
  busy.value = true
  try {
    await securityApi.verifyContact(contact.id)
    notice.ok(t('contacts.noticeVerificationSent'))
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('contacts.errorVerify'))
  } finally {
    busy.value = false
  }
}

async function makePrimary(contact: Contact) {
  busy.value = true
  try {
    await securityApi.setPrimaryContact(contact.id)
    notice.ok(t('contacts.noticePrimary'))
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('contacts.errorPrimary'))
  } finally {
    busy.value = false
  }
}

async function togglePublic(contact: Contact) {
  busy.value = true
  try {
    await securityApi.setContactPublic(contact.id, !contact.is_public)
    notice.ok(
      contact.is_public ? t('contacts.noticeHidden') : t('contacts.noticeShown'),
    )
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('contacts.errorVisibility'))
  } finally {
    busy.value = false
  }
}

async function remove() {
  const target = pendingDelete.value
  if (!target) return
  busy.value = true
  try {
    await securityApi.deleteContact(target.id)
    notice.ok(t('contacts.noticeDeleted'))
    pendingDelete.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('contacts.errorDelete'))
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      :title="t('contacts.title')"
      :description="t('contacts.description')"
    />

    <SecurityNav />

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
      <ConsolePanel :title="t('contacts.contacts')" :note="t('contacts.totalNote', { n: contacts.length })">
        <div v-if="loading" class="py-6 text-xs text-faint">{{ t('contacts.reading') }}</div>

        <p v-else-if="!contacts.length" class="voice py-6">
          {{ t('contacts.empty') }}
        </p>

        <ul v-else class="divide-y divide-line-soft">
          <li
            v-for="contact in contacts"
            :key="contact.id"
            class="flex flex-wrap items-start justify-between gap-3 py-3 first:pt-0 last:pb-0"
          >
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <span class="break-all text-xs text-base-content">{{ contact.content }}</span>
                <span class="chip">{{ contactTypeLabel(contact.type) }}</span>
                <span v-if="contact.is_primary" class="chip text-primary">{{ t('contacts.primary') }}</span>
                <span v-if="contact.is_public" class="chip text-accent">{{ t('contacts.public') }}</span>
                <span
                  class="chip"
                  :class="contact.verified_at ? 'text-success' : 'text-warning'"
                >
                  {{ contact.verified_at ? t('contacts.verified') : t('contacts.unverified') }}
                </span>
              </div>
              <p class="mt-1 text-2xs text-faint">
                {{ t('contacts.added', { time: relative(contact.created_at) }) }}
                <template v-if="contact.verified_at">
                  · {{ t('contacts.verifiedAt', { date: utcStamp(contact.verified_at).slice(0, 10) }) }}
                </template>
              </p>
            </div>
            <div class="flex flex-wrap items-center gap-3">
              <button
                v-if="!contact.verified_at"
                type="button"
                class="text-2xs text-dim hover:text-base-content"
                :disabled="busy"
                @click="verify(contact)"
              >
                {{ t('contacts.verify') }}
              </button>
              <button
                v-if="!contact.is_primary"
                type="button"
                class="text-2xs text-dim hover:text-base-content"
                :disabled="busy"
                @click="makePrimary(contact)"
              >
                {{ t('contacts.makePrimary') }}
              </button>
              <button
                type="button"
                class="text-2xs text-dim hover:text-base-content"
                :disabled="busy"
                @click="togglePublic(contact)"
              >
                {{ contact.is_public ? t('contacts.hide') : t('contacts.showPublicly') }}
              </button>
              <button
                type="button"
                class="text-2xs text-dim hover:text-error"
                :disabled="busy"
                @click="pendingDelete = contact"
              >
                {{ t('common.delete') }}
              </button>
            </div>
          </li>
        </ul>
      </ConsolePanel>

      <ConsolePanel :title="t('contacts.addContact')">
        <label for="contact-type" class="field-label">{{ t('contacts.type') }}</label>
        <select id="contact-type" v-model="type" class="select select-sm">
          <option :value="0">{{ contactTypeLabel(0) }}</option>
          <option :value="1">{{ contactTypeLabel(1) }}</option>
          <option :value="2">{{ contactTypeLabel(2) }}</option>
        </select>

        <label for="contact-content" class="field-label mt-4">{{ t('contacts.value') }}</label>
        <input
          id="contact-content"
          v-model="content"
          class="input input-sm"
          :placeholder="type === 0 ? t('contacts.emailPlaceholder') : t('contacts.valuePlaceholder')"
          autocapitalize="off"
          spellcheck="false"
        />

        <button
          type="button"
          class="btn btn-primary btn-sm mt-4 w-full"
          :disabled="busy || !content.trim()"
          @click="add"
        >
          {{ t('contacts.addAction') }}
        </button>

        <p class="mt-3 voice text-[12px] text-faint">
          {{ t('contacts.elevatedHint') }}
        </p>
      </ConsolePanel>
    </div>

    <ConfirmAction
      v-if="pendingDelete"
      :title="t('contacts.deleteTitle')"
      :body="t('contacts.deleteBody', { contact: pendingDelete.content })"
      :action="t('contacts.deleteAction')"
      tone="error"
      :busy="busy"
      @cancel="pendingDelete = null"
      @confirm="remove"
    />
  </div>
</template>
