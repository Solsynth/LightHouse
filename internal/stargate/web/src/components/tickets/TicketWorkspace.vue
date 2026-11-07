<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowLeft16, Document16 } from '@carbon/icons-vue'
import { RouterLink } from 'vue-router'

import { accountApi, ticketApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { Account, Ticket, TicketMessage } from '@/lib/api/types'
import { useNoticeStore } from '@/stores/notice'
import { relative, shortId } from '@/lib/format'
import { ticketStatusLabel, ticketTypeLabel } from '@/lib/enums'
import { TICKET_STATUSES, statusRail } from '@/lib/tickets'
import AccountAvatar from '@/components/AccountAvatar.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import TextField from '@/components/TextField.vue'
import TicketPriority from '@/components/tickets/TicketPriority.vue'
import TicketStatus from '@/components/tickets/TicketStatus.vue'

const props = withDefaults(
  defineProps<{
    id: string
    admin?: boolean
  }>(),
  { admin: false },
)

const emit = defineEmits<{ changed: [] }>()

const { t } = useI18n()
const notice = useNoticeStore()

const ticket = ref<Ticket | null>(null)
const loading = ref(true)
const error = ref('')
const reply = ref('')
const statusDraft = ref(0)
const assignName = ref('')
const busy = ref<'reply' | 'status' | 'assign' | 'unassign' | null>(null)

const messages = computed(() =>
  [...(ticket.value?.messages ?? [])].sort((a, b) =>
    (a.created_at ?? '').localeCompare(b.created_at ?? ''),
  ),
)

const resources = computed(() =>
  (ticket.value?.resources ?? []).filter((value): value is string => Boolean(value)),
)

function accountLabel(account?: Account): string {
  if (!account) return t('common.unknown')
  return account.nick || account.name || shortId(account.id)
}

function isStaff(message: TicketMessage): boolean {
  return message.sender_id !== ticket.value?.creator_id
}

function isLink(value: string): boolean {
  return /^https?:\/\//i.test(value)
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const loaded = await ticketApi.get(props.id)
    ticket.value = loaded
    statusDraft.value = loaded.status
    assignName.value = ''
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('support.detail.errorLoad')
    ticket.value = null
  } finally {
    loading.value = false
  }
}

async function applyStatus(event: Event) {
  const current = ticket.value
  if (!current) return
  const next = Number((event.target as HTMLSelectElement).value)
  if (next === current.status) return
  busy.value = 'status'
  try {
    const updated = await ticketApi.setStatus(props.id, next)
    ticket.value = updated
    statusDraft.value = updated.status
    notice.ok(t('support.notice.status', { status: ticketStatusLabel(updated.status) }))
    emit('changed')
  } catch (caught) {
    statusDraft.value = current.status
    notice.fail(caught instanceof StargateError ? caught.message : t('support.error.status'))
  } finally {
    busy.value = null
  }
}

async function assign() {
  const name = assignName.value.trim()
  if (!name || busy.value) return
  busy.value = 'assign'
  try {
    const account = await accountApi.byName(name)
    const updated = await ticketApi.assign(props.id, account.id)
    ticket.value = updated
    assignName.value = ''
    notice.ok(t('support.notice.assigned', { account: accountLabel(account) }))
    emit('changed')
  } catch (caught) {
    notice.fail(caught instanceof StargateError ? caught.message : t('support.error.assign'))
  } finally {
    busy.value = null
  }
}

async function unassign() {
  if (busy.value) return
  busy.value = 'unassign'
  try {
    const updated = await ticketApi.assign(props.id, null)
    ticket.value = updated
    notice.ok(t('support.notice.unassigned'))
    emit('changed')
  } catch (caught) {
    notice.fail(caught instanceof StargateError ? caught.message : t('support.error.assign'))
  } finally {
    busy.value = null
  }
}

async function sendReply() {
  const content = reply.value.trim()
  if (!content) {
    notice.fail(t('support.detail.replyRequired'))
    return
  }
  busy.value = 'reply'
  try {
    await ticketApi.reply(props.id, { content })
    reply.value = ''
    ticket.value = await ticketApi.get(props.id)
    statusDraft.value = ticket.value.status
    notice.ok(t('support.notice.replied'))
    emit('changed')
  } catch (caught) {
    notice.fail(caught instanceof StargateError ? caught.message : t('support.error.reply'))
  } finally {
    busy.value = null
  }
}

watch(
  () => props.id,
  () => {
    void load()
  },
  { immediate: true },
)
</script>

<template>
  <div>
    <div v-if="loading">
      <LoadingSkeleton :rows="5" :columns="[3, 2, 1]" header />
    </div>

    <div v-else-if="error || !ticket" class="rounded-card bg-base-200/50 px-6 py-7">
      <p class="text-sm font-semibold text-base-content">{{ t('support.detail.notFound') }}</p>
      <p class="voice mt-2 text-[13px]">{{ error }}</p>
      <RouterLink to="/support" class="btn btn-ghost btn-sm mt-4 lg:hidden">
        <ArrowLeft16 aria-hidden="true" />
        {{ t('support.backToRequests') }}
      </RouterLink>
    </div>

    <div v-else>
      <RouterLink to="/support" class="btn btn-quiet btn-xs -ml-2 mb-4 lg:hidden">
        <ArrowLeft16 aria-hidden="true" />
        {{ t('support.backToRequests') }}
      </RouterLink>

      <header class="relative pl-4">
        <span
          class="absolute bottom-2 left-0 top-1 w-[3px] rounded-full"
          :class="statusRail(ticket.status)"
          aria-hidden="true"
        />

        <div class="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-2xs text-faint">
          <span class="font-mono text-[11px]">#{{ shortId(ticket.id) }}</span>
          <TicketStatus :status="ticket.status" quiet />
          <TicketPriority :priority="ticket.priority" />
          <span class="chip">{{ ticketTypeLabel(ticket.type) }}</span>
        </div>

        <h2 class="title mt-2.5 max-w-2xl text-xl text-base-content sm:text-2xl">
          {{ ticket.title }}
        </h2>

        <p class="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-2xs text-faint">
          <span>{{ t('support.detail.openedBy', { account: accountLabel(ticket.creator) }) }}</span>
          <span aria-hidden="true">/</span>
          <span>{{ t('support.detail.opened', { time: relative(ticket.created_at) }) }}</span>
          <template v-if="admin">
            <span aria-hidden="true">/</span>
            <span>
              {{ t('support.detail.assignee') }}:
              {{ ticket.assignee ? accountLabel(ticket.assignee) : t('support.detail.unassigned') }}
            </span>
          </template>
        </p>
      </header>

      <section
        v-if="admin"
        class="mt-5 rounded-card bg-base-200/50 px-5 py-4"
        :aria-label="t('support.detail.manage')"
      >
        <h3 class="text-sm font-semibold text-base-content">{{ t('support.detail.manage') }}</h3>
        <div class="mt-3 grid gap-3 sm:grid-cols-2">
          <TextField
            id="ticket-admin-status"
            v-model.number="statusDraft"
            as="select"
            :label="t('support.detail.status')"
            :disabled="busy === 'status'"
            @change="applyStatus"
          >
            <option v-for="status in TICKET_STATUSES" :key="status" :value="status">
              {{ ticketStatusLabel(status) }}
            </option>
          </TextField>

          <div>
            <p class="field-label">{{ t('support.detail.assigneeField') }}</p>
            <div v-if="ticket.assignee" class="flex items-center justify-between gap-2">
              <span class="min-w-0 truncate text-[13px] text-dim">
                {{ accountLabel(ticket.assignee) }}
              </span>
              <button
                type="button"
                class="btn btn-ghost btn-xs text-error"
                :disabled="busy !== null"
                @click="unassign"
              >
                {{ t('support.detail.unassign') }}
              </button>
            </div>
            <form v-else class="flex items-end gap-2" @submit.prevent="assign">
              <TextField
                id="ticket-assignee"
                v-model="assignName"
                root-class="min-w-0 flex-1"
                :label="t('support.detail.assignee')"
                :placeholder="t('support.detail.assignPlaceholder')"
                autocapitalize="off"
                spellcheck="false"
              />
              <button
                type="submit"
                class="btn btn-primary btn-field px-4"
                :disabled="busy !== null || !assignName.trim()"
              >
                {{ busy === 'assign' ? t('support.detail.assigning') : t('support.detail.assign') }}
              </button>
            </form>
          </div>
        </div>
      </section>

      <section class="mt-6" :aria-label="t('support.detail.conversation')">
        <div class="flex items-baseline justify-between gap-3">
          <h3 class="text-sm font-semibold text-base-content">
            {{ t('support.detail.conversation') }}
          </h3>
          <span class="text-2xs text-faint">
            {{ t('support.detail.messageCount', { n: messages.length }) }}
          </span>
        </div>

        <p v-if="!messages.length" class="voice mt-3 text-[13px]">
          {{ t('support.detail.noMessages') }}
        </p>

        <ol v-else class="mt-3 space-y-2.5">
          <li
            v-for="message in messages"
            :key="message.id"
            class="rounded-card px-5 py-4"
            :class="isStaff(message) ? 'bg-tint' : 'bg-raised'"
          >
            <header class="flex flex-wrap items-center gap-2 text-2xs text-faint">
              <AccountAvatar :account="message.sender" />
              <span class="text-dim">{{ accountLabel(message.sender) }}</span>
              <span v-if="isStaff(message)" class="chip text-primary">
                {{ t('support.detail.staff') }}
              </span>
              <time class="ml-auto">{{ relative(message.created_at) }}</time>
            </header>

            <p class="mt-2.5 whitespace-pre-wrap text-[14px] leading-relaxed text-dim">
              {{ message.content }}
            </p>

            <ul v-if="message.files?.length" class="mt-3 space-y-1">
              <li v-for="file in message.files" :key="file.id">
                <a
                  :href="file.url"
                  target="_blank"
                  rel="noopener noreferrer"
                  class="inline-flex items-center gap-1.5 text-2xs text-info hover:underline"
                >
                  <Document16 aria-hidden="true" />
                  {{ file.name || file.id }}
                </a>
              </li>
            </ul>
          </li>
        </ol>
      </section>

      <ul v-if="resources.length" class="mt-5 space-y-1">
        <li class="text-2xs font-semibold text-dim">{{ t('support.detail.resources') }}</li>
        <li v-for="resource in resources" :key="resource">
          <a
            v-if="isLink(resource)"
            :href="resource"
            target="_blank"
            rel="noopener noreferrer"
            class="break-all text-2xs text-info hover:underline"
          >
            {{ resource }}
          </a>
          <span v-else class="break-all font-mono text-2xs text-faint">{{ resource }}</span>
        </li>
      </ul>

      <form class="mt-6 rounded-card bg-base-200/50 px-5 py-4" @submit.prevent="sendReply">
        <TextField
          id="ticket-reply"
          v-model="reply"
          as="textarea"
          :rows="3"
          :label="t('support.detail.reply')"
          :placeholder="t('support.detail.replyPlaceholder')"
          maxlength="16384"
        />
        <div class="mt-3 flex justify-end">
          <button
            type="submit"
            class="btn btn-primary btn-sm"
            :disabled="busy !== null || !reply.trim()"
          >
            {{ busy === 'reply' ? t('support.detail.sending') : t('support.detail.send') }}
          </button>
        </div>
      </form>
    </div>
  </div>
</template>
