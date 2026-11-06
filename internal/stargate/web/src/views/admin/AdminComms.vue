<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { useNoticeStore } from '@/stores/notice'
import { accountApi, adminApi } from '@/lib/api'
import PageHeader from '@/components/PageHeader.vue'
import AdminNav from '@/components/AdminNav.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import TextField from '@/components/TextField.vue'

type TargetMode = 'single' | 'broadcast'

const { t } = useI18n()
const notice = useNoticeStore()

const targetMode = ref<TargetMode>('single')
const accountNames = ref('')
const busy = ref<'notify' | 'email' | 'export' | null>(null)

const topic = ref('admin.broadcast')
const title = ref('')
const subtitle = ref('')
const body = ref('')
const actionUri = ref('')
const pushType = ref('')
const isSilent = ref(false)
const isSavable = ref(true)

const nightOwlName = ref('')
const nightOwlHour = ref(7)

const subject = ref('')
const htmlBody = ref('')

const targetHint = computed(() =>
  targetMode.value === 'broadcast'
    ? t('admin.comms.targetBroadcast')
    : t('admin.comms.targetSingle'),
)

async function resolveIds(): Promise<string[]> {
  if (targetMode.value === 'broadcast') return []
  const names = accountNames.value
    .split(/[\s,]+/)
    .map((name) => name.trim())
    .filter(Boolean)
  if (!names.length) throw new Error(t('admin.comms.targetRequired'))
  const ids: string[] = []
  for (const name of names) {
    const account = await accountApi.byName(name)
    ids.push(account.id)
  }
  return ids
}

function targetBody(ids: string[]) {
  if (targetMode.value === 'broadcast') return { broadcast_to_all: true }
  return ids.length === 1 ? { account_id: ids[0] } : { account_ids: ids }
}

function reportDispatch(result: { sent: number; skipped: number; resolved: number }) {
  notice.ok(
    t('admin.comms.noticeDispatched', {
      sent: result.sent,
      skipped: result.skipped,
      resolved: result.resolved,
    }),
  )
}

async function sendNotification() {
  busy.value = 'notify'
  try {
    const ids = await resolveIds()
    const result = await adminApi.notify({
      ...targetBody(ids),
      topic: topic.value.trim(),
      title: title.value.trim() || undefined,
      subtitle: subtitle.value.trim() || undefined,
      body: body.value.trim() || undefined,
      action_uri: actionUri.value.trim() || undefined,
      push_type: pushType.value.trim() || undefined,
      is_silent: isSilent.value,
      is_savable: isSavable.value,
    })
    reportDispatch(result)
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.comms.actionFailed'))
  } finally {
    busy.value = null
  }
}

async function sendEmail() {
  busy.value = 'email'
  try {
    const ids = await resolveIds()
    const result = await adminApi.sendEmails({
      ...targetBody(ids),
      subject: subject.value.trim(),
      html_body: htmlBody.value,
    })
    reportDispatch(result)
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.comms.actionFailed'))
  } finally {
    busy.value = null
  }
}

async function sendNightOwl() {
  busy.value = 'notify'
  try {
    const account = await accountApi.byName(nightOwlName.value.trim())
    await adminApi.nightOwl(account.name, nightOwlHour.value)
    notice.ok(t('admin.comms.noticeNightOwl', { account: account.nick || account.name }))
    nightOwlName.value = ''
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.comms.actionFailed'))
  } finally {
    busy.value = null
  }
}

async function exportEmails() {
  busy.value = 'export'
  try {
    const ids = await resolveIds()
    const query =
      targetMode.value === 'broadcast'
        ? { broadcastToAll: true }
        : ids.length === 1
          ? { accountId: ids[0] }
          : { accountIds: ids }
    const { blob, filename } = await adminApi.exportEmails(query)
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = filename
    anchor.click()
    URL.revokeObjectURL(url)
    notice.ok(t('admin.comms.noticeExported'))
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.comms.actionFailed'))
  } finally {
    busy.value = null
  }
}
</script>

<template>
  <div>
    <PageHeader :title="t('admin.comms.title')" :description="t('admin.comms.description')" />
    <AdminNav />

    <ConsolePanel :title="t('admin.comms.targetTitle')" :note="targetHint">
      <div class="flex flex-wrap items-center gap-2">
        <label
          v-for="mode in ['single', 'broadcast'] as const"
          :key="mode"
          class="chip cursor-pointer"
          :class="targetMode === mode ? 'chip-active' : ''"
        >
          <input
            v-model="targetMode"
            class="sr-only"
            type="radio"
            name="target-mode"
            :value="mode"
          />
          {{ mode === 'single' ? t('admin.comms.targetAccounts') : t('admin.comms.targetEveryone') }}
        </label>
      </div>
      <TextField
        v-if="targetMode === 'single'"
        id="comms-targets"
        v-model="accountNames"
        root-class="mt-4"
        :label="t('admin.comms.targetLabel')"
        :hint="t('admin.comms.targetHint')"
        :placeholder="'alice, bob'"
        autocapitalize="off"
      />
    </ConsolePanel>

    <ConsolePanel class="mt-6" :title="t('admin.comms.nightOwlTitle')">
      <p class="voice mb-3 text-[12px] text-faint">{{ t('admin.comms.nightOwlHint') }}</p>
      <form class="flex flex-wrap items-end gap-2" @submit.prevent="sendNightOwl">
        <TextField
          id="night-owl-account"
          v-model="nightOwlName"
          root-class="min-w-[200px] flex-1"
          :label="t('admin.comms.accountName')"
        />
        <TextField
          id="night-owl-hour"
          v-model.number="nightOwlHour"
          type="number"
          root-class="w-28"
          :label="t('admin.comms.nightOwlHour')"
        />
        <button
          type="submit"
          class="btn btn-ghost btn-field"
          :disabled="busy !== null || !nightOwlName.trim()"
        >
          {{ t('admin.comms.sendNightOwl') }}
        </button>
      </form>
    </ConsolePanel>

    <div class="mt-6 grid items-start gap-6 lg:grid-cols-2">
      <ConsolePanel :title="t('admin.comms.notificationTitle')">
        <form class="space-y-4" @submit.prevent="sendNotification">
          <TextField
            id="comms-topic"
            v-model="topic"
            :label="t('admin.comms.topic')"
            :placeholder="'admin.broadcast'"
          />
          <TextField id="comms-title" v-model="title" :label="t('admin.comms.titleField')" />
          <TextField
            id="comms-subtitle"
            v-model="subtitle"
            :label="t('admin.comms.subtitle')"
          />
          <TextField
            id="comms-body"
            v-model="body"
            as="textarea"
            :rows="3"
            :label="t('admin.comms.body')"
          />
          <TextField
            id="comms-action"
            v-model="actionUri"
            :label="t('admin.comms.actionUri')"
            placeholder="solarpass://notifications"
          />
          <div class="flex flex-wrap items-center gap-4 text-2xs text-dim">
            <label class="flex items-center gap-2">
              <input v-model="isSilent" class="checkbox checkbox-xs" type="checkbox" />
              {{ t('admin.comms.silent') }}
            </label>
            <label class="flex items-center gap-2">
              <input v-model="isSavable" class="checkbox checkbox-xs" type="checkbox" />
              {{ t('admin.comms.savable') }}
            </label>
          </div>
          <button
            type="submit"
            class="btn btn-primary btn-sm"
            :disabled="busy !== null || !topic.trim()"
          >
            {{ t('admin.comms.sendNotification') }}
          </button>
        </form>
      </ConsolePanel>

      <ConsolePanel :title="t('admin.comms.emailTitle')">
        <form class="space-y-4" @submit.prevent="sendEmail">
          <TextField
            id="comms-subject"
            v-model="subject"
            :label="t('admin.comms.subject')"
          />
          <TextField
            id="comms-html"
            v-model="htmlBody"
            as="textarea"
            :rows="10"
            :label="t('admin.comms.htmlBody')"
            :hint="t('admin.comms.htmlHint')"
          />
          <div class="flex flex-wrap items-center gap-2">
            <button
              type="submit"
              class="btn btn-primary btn-sm"
              :disabled="busy !== null || !subject.trim() || !htmlBody.trim()"
            >
                {{ t('admin.comms.sendEmail') }}
            </button>
            <button
              type="button"
              class="btn btn-ghost btn-sm"
              :disabled="busy !== null"
              @click="exportEmails"
            >
              {{ t('admin.comms.export') }}
            </button>
          </div>
        </form>
      </ConsolePanel>
    </div>
  </div>
</template>
