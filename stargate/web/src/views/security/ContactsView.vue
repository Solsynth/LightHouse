<script setup lang="ts">
import { onMounted, ref } from 'vue'

import { useNoticeStore } from '@/stores/notice'
import { securityApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { Contact } from '@/lib/api/types'
import { contactTypeLabels } from '@/lib/enums'
import { relative, utcStamp } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import SecurityNav from '@/components/SecurityNav.vue'

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
    error.value = caught instanceof StargateError ? caught.message : 'Contacts could not be loaded.'
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
    notice.ok('Contact added. Verify it to use it for codes and recovery.')
    await load()
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : 'The contact could not be added.'
  } finally {
    busy.value = false
  }
}

async function verify(contact: Contact) {
  busy.value = true
  try {
    await securityApi.verifyContact(contact.id)
    notice.ok('Verification email sent.')
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : 'Verification could not be requested.')
  } finally {
    busy.value = false
  }
}

async function makePrimary(contact: Contact) {
  busy.value = true
  try {
    await securityApi.setPrimaryContact(contact.id)
    notice.ok('Primary contact updated.')
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : 'The primary contact could not be set.')
  } finally {
    busy.value = false
  }
}

async function togglePublic(contact: Contact) {
  busy.value = true
  try {
    await securityApi.setContactPublic(contact.id, !contact.is_public)
    notice.ok(contact.is_public ? 'Contact hidden from your profile.' : 'Contact shown on your profile.')
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : 'Visibility could not be changed.')
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
    notice.ok('Contact deleted.')
    pendingDelete.value = null
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : 'The contact could not be deleted.')
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      title="Contacts"
      description="Verified contacts receive sign-in codes and password-reset links. Public contacts appear on your profile."
    />

    <SecurityNav />

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
      <ConsolePanel title="Contacts" :note="`${contacts.length} total`">
        <div v-if="loading" class="py-6 text-xs text-faint">Reading contact table…</div>

        <p v-else-if="!contacts.length" class="voice py-6">
          No contacts yet. Add an email address so this account can receive codes.
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
                <span class="chip">{{ contactTypeLabels[contact.type] }}</span>
                <span v-if="contact.is_primary" class="chip text-primary">primary</span>
                <span v-if="contact.is_public" class="chip text-accent">public</span>
                <span
                  class="chip"
                  :class="contact.verified_at ? 'text-success' : 'text-warning'"
                >
                  {{ contact.verified_at ? 'verified' : 'unverified' }}
                </span>
              </div>
              <p class="mt-1 text-2xs text-faint">
                added {{ relative(contact.created_at) }}
                <template v-if="contact.verified_at">
                  · verified {{ utcStamp(contact.verified_at).slice(0, 10) }}
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
                Verify
              </button>
              <button
                v-if="!contact.is_primary"
                type="button"
                class="text-2xs text-dim hover:text-base-content"
                :disabled="busy"
                @click="makePrimary(contact)"
              >
                Make Primary
              </button>
              <button
                type="button"
                class="text-2xs text-dim hover:text-base-content"
                :disabled="busy"
                @click="togglePublic(contact)"
              >
                {{ contact.is_public ? 'Hide' : 'Show Publicly' }}
              </button>
              <button
                type="button"
                class="text-2xs text-dim hover:text-error"
                :disabled="busy"
                @click="pendingDelete = contact"
              >
                Delete
              </button>
            </div>
          </li>
        </ul>
      </ConsolePanel>

      <ConsolePanel title="Add a Contact">
        <label for="contact-type" class="field-label">Type</label>
        <select id="contact-type" v-model="type" class="select select-sm">
          <option :value="0">email</option>
          <option :value="1">phone</option>
          <option :value="2">address</option>
        </select>

        <label for="contact-content" class="field-label mt-4">Value</label>
        <input
          id="contact-content"
          v-model="content"
          class="input input-sm"
          :placeholder="type === 0 ? 'name@example.com' : 'value'"
          autocapitalize="off"
          spellcheck="false"
        />

        <button
          type="button"
          class="btn btn-primary btn-sm mt-4 w-full"
          :disabled="busy || !content.trim()"
          @click="add"
        >
          Add Contact
        </button>

        <p class="mt-3 voice text-[12px] text-faint">
          Adding a contact requires an elevated session. Verification is sent as a one-time link.
        </p>
      </ConsolePanel>
    </div>

    <ConfirmAction
      v-if="pendingDelete"
      title="Delete This Contact?"
      :body="`${pendingDelete.content} will stop receiving codes. This cannot be undone.`"
      action="Delete Contact"
      tone="error"
      :busy="busy"
      @cancel="pendingDelete = null"
      @confirm="remove"
    />
  </div>
</template>
