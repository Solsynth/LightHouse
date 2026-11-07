<script setup lang="ts">
import { inject, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter, RouterLink } from 'vue-router'
import { ArrowLeft16 } from '@carbon/icons-vue'

import { ticketApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import { useNoticeStore } from '@/stores/notice'
import { ticketPriorityLabel, ticketTypeLabel } from '@/lib/enums'
import { shortId } from '@/lib/format'
import {
  TICKET_PRIORITIES,
  TICKET_PRIORITY,
  TICKET_REFRESH,
  TICKET_TYPES,
  TICKET_TYPE,
} from '@/lib/tickets'
import TextField from '@/components/TextField.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const notice = useNoticeStore()
const refresh = inject(TICKET_REFRESH, async () => {})

function initialType(): number {
  const value = Number(route.query.type)
  return (TICKET_TYPES as readonly number[]).includes(value) ? value : TICKET_TYPE.support
}

const title = ref('')
const content = ref('')
const type = ref(initialType())
const priority = ref<number>(TICKET_PRIORITY.medium)
const busy = ref(false)
const error = ref('')

async function submit() {
  const trimmedTitle = title.value.trim()
  const trimmedContent = content.value.trim()
  if (trimmedTitle.length < 3) {
    error.value = t('support.new.errorTitle')
    return
  }
  if (!trimmedContent) {
    error.value = t('support.new.errorContent')
    return
  }
  busy.value = true
  error.value = ''
  try {
    const ticket = await ticketApi.create({
      title: trimmedTitle,
      content: trimmedContent,
      type: type.value,
      priority: priority.value,
    })
    notice.ok(t('support.notice.created', { id: shortId(ticket.id) }))
    await refresh()
    await router.replace({ name: 'support-ticket', params: { id: ticket.id } })
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('support.error.create')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div>
    <RouterLink :to="{ name: 'support' }" class="btn btn-quiet btn-xs -ml-2 mb-4 lg:hidden">
      <ArrowLeft16 aria-hidden="true" />
      {{ t('support.backToRequests') }}
    </RouterLink>

    <h2 class="title text-xl text-base-content">{{ t('support.new.title') }}</h2>
    <p class="voice mt-2 max-w-xl text-[13.5px]">{{ t('support.new.description') }}</p>

    <p v-if="error" class="note note-error mt-4">{{ error }}</p>

    <form class="mt-5 rounded-card bg-base-200/50 px-5 py-5" @submit.prevent="submit">
      <TextField
        id="ticket-title"
        v-model="title"
        :label="t('support.new.fieldTitle')"
        :placeholder="t('support.new.titlePlaceholder')"
        :hint="t('support.new.titleHint')"
        maxlength="256"
        required
      />

      <div class="mt-4 grid gap-3 sm:grid-cols-2">
        <TextField
          id="ticket-type"
          v-model.number="type"
          as="select"
          :label="t('support.typeLabel')"
        >
          <option v-for="option in TICKET_TYPES" :key="option" :value="option">
            {{ ticketTypeLabel(option) }}
          </option>
        </TextField>

        <TextField
          id="ticket-priority"
          v-model.number="priority"
          as="select"
          :label="t('support.priorityLabel')"
        >
          <option v-for="option in TICKET_PRIORITIES" :key="option" :value="option">
            {{ ticketPriorityLabel(option) }}
          </option>
        </TextField>
      </div>

      <TextField
        id="ticket-content"
        v-model="content"
        root-class="mt-4"
        as="textarea"
        :rows="8"
        :label="t('support.new.content')"
        :placeholder="t('support.new.contentPlaceholder')"
        maxlength="16384"
        required
      />

      <div class="mt-5 flex flex-wrap items-center justify-end gap-2">
        <RouterLink :to="{ name: 'support' }" class="btn btn-ghost btn-sm">
          {{ t('support.new.cancel') }}
        </RouterLink>
        <button type="submit" class="btn btn-primary btn-sm" :disabled="busy">
          {{ busy ? t('support.new.creating') : t('support.new.create') }}
        </button>
      </div>
    </form>
  </div>
</template>
