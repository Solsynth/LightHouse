<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Renew16 } from '@carbon/icons-vue'

import { useNoticeStore } from '@/stores/notice'
import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { ProgressionDefinition } from '@/lib/api/types'
import { relative } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import AdminNav from '@/components/AdminNav.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import IconButton from '@/components/IconButton.vue'
import TabBox from '@/components/TabBox.vue'
import TextField from '@/components/TextField.vue'
import type { Tab } from '@/components/tab'

type Kind = 'achievements' | 'quests'

interface Draft {
  identifier: string
  title: string
  summary: string
  icon: string
  series_identifier: string
  series_title: string
  series_order: string
  sort_order: string
  target_count: string
  hidden: boolean
  is_progress_enabled: boolean
  available_from: string
  available_until: string
  trigger: string
  schedule: string
  reward: string
}

const { t } = useI18n()
const notice = useNoticeStore()

const kind = ref<Kind>('achievements')
const achievements = ref<ProgressionDefinition[]>([])
const quests = ref<ProgressionDefinition[]>([])
const loading = ref(true)
const busy = ref(false)
const error = ref('')
const editorOpen = ref(false)
const editing = ref<ProgressionDefinition | null>(null)
const draft = ref<Draft>(emptyDraft())

const tabs = computed<Tab[]>(() => [
  { key: 'achievements', label: t('admin.progression.tabs.achievements') },
  { key: 'quests', label: t('admin.progression.tabs.quests') },
])

const current = computed(() => (kind.value === 'achievements' ? achievements.value : quests.value))

function emptyDraft(): Draft {
  return {
    identifier: '',
    title: '',
    summary: '',
    icon: '',
    series_identifier: '',
    series_title: '',
    series_order: '',
    sort_order: '0',
    target_count: '1',
    hidden: false,
    is_progress_enabled: true,
    available_from: '',
    available_until: '',
    trigger: '{}',
    schedule: '{}',
    reward: '{}',
  }
}

function toLocalInput(value?: string): string {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  const offset = date.getTimezoneOffset() * 60_000
  return new Date(date.getTime() - offset).toISOString().slice(0, 16)
}

function draftFrom(definition: ProgressionDefinition): Draft {
  return {
    identifier: definition.identifier,
    title: definition.title,
    summary: definition.summary,
    icon: definition.icon ?? '',
    series_identifier: definition.series_identifier ?? '',
    series_title: definition.series_title ?? '',
    series_order: definition.series_order != null ? String(definition.series_order) : '',
    sort_order: String(definition.sort_order),
    target_count: String(definition.target_count),
    hidden: definition.hidden,
    is_progress_enabled: definition.is_progress_enabled,
    available_from: toLocalInput(definition.available_from),
    available_until: toLocalInput(definition.available_until),
    trigger: JSON.stringify(definition.trigger ?? {}, null, 2),
    schedule: JSON.stringify(definition.schedule ?? {}, null, 2),
    reward: JSON.stringify(definition.reward ?? {}, null, 2),
  }
}

function openCreate() {
  editing.value = null
  draft.value = emptyDraft()
  editorOpen.value = true
}

function openEdit(definition: ProgressionDefinition) {
  editing.value = definition
  draft.value = draftFrom(definition)
  editorOpen.value = true
}

function parseJson(value: string, field: string): Record<string, unknown> {
  const text = value.trim()
  if (!text) return {}
  try {
    return JSON.parse(text) as Record<string, unknown>
  } catch {
    throw new Error(t('admin.progression.invalidJson', { field }))
  }
}

function isoOrNull(value: string): string | null {
  if (!value) return null
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return null
  return date.toISOString()
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [achievementList, questList] = await Promise.all([
      adminApi.achievements(),
      adminApi.quests(),
    ])
    achievements.value = achievementList
    quests.value = questList
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('admin.progression.errorLoad')
  } finally {
    loading.value = false
  }
}

async function save() {
  busy.value = true
  try {
    const value = draft.value
    const body = {
      identifier: value.identifier.trim(),
      title: value.title.trim(),
      summary: value.summary,
      icon: value.icon.trim() || null,
      series_identifier: value.series_identifier.trim() || null,
      series_title: value.series_title.trim() || null,
      series_order: value.series_order ? Number(value.series_order) : null,
      sort_order: Number(value.sort_order) || 0,
      hidden: value.hidden,
      is_enabled: editing.value?.is_enabled ?? true,
      is_seed_managed: editing.value?.is_seed_managed ?? false,
      is_progress_enabled: value.is_progress_enabled,
      available_from: isoOrNull(value.available_from),
      available_until: isoOrNull(value.available_until),
      target_count: Number(value.target_count) || 0,
      trigger: parseJson(value.trigger, 'trigger'),
      schedule: kind.value === 'quests' ? parseJson(value.schedule, 'schedule') : null,
      reward: parseJson(value.reward, 'reward'),
    }
    const save =
      kind.value === 'achievements'
        ? editing.value
          ? adminApi.updateAchievement(editing.value.identifier, body)
          : adminApi.createAchievement(body)
        : editing.value
          ? adminApi.updateQuest(editing.value.identifier, body)
          : adminApi.createQuest(body)
    await save
    notice.ok(t('admin.progression.noticeSaved'))
    editorOpen.value = false
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.progression.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function toggle(definition: ProgressionDefinition) {
  busy.value = true
  try {
    if (kind.value === 'achievements') await adminApi.toggleAchievement(definition.identifier)
    else await adminApi.toggleQuest(definition.identifier)
    notice.ok(
      definition.is_enabled
        ? t('admin.progression.noticeDisabled')
        : t('admin.progression.noticeEnabled'),
    )
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.progression.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function sync() {
  busy.value = true
  try {
    await adminApi.syncProgression()
    notice.ok(t('admin.progression.noticeSynced'))
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.progression.actionFailed'))
  } finally {
    busy.value = false
  }
}

async function testPacket() {
  busy.value = true
  try {
    await adminApi.testProgressionPacket()
    notice.ok(t('admin.progression.noticePacket'))
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('admin.progression.actionFailed'))
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      :title="t('admin.progression.title')"
      :description="t('admin.progression.description')"
    >
      <template #actions>
        <IconButton :label="t('common.refresh')" :disabled="loading" @click="load">
          <Renew16 aria-hidden="true" />
        </IconButton>
      </template>
    </PageHeader>

    <AdminNav />

    <div class="mb-5 flex flex-wrap items-center justify-between gap-3">
      <TabBox
        :tabs="tabs"
        :active="kind"
        :label="t('admin.progression.tabsLabel')"
        @select="kind = $event as Kind"
      />
      <div class="flex flex-wrap items-center gap-2">
        <button type="button" class="btn btn-ghost btn-sm" :disabled="busy" @click="sync">
          {{ t('admin.progression.sync') }}
        </button>
        <button type="button" class="btn btn-ghost btn-sm" :disabled="busy" @click="testPacket">
          {{ t('admin.progression.testPacket') }}
        </button>
        <button type="button" class="btn btn-primary btn-sm" :disabled="busy" @click="openCreate">
          {{ t('admin.progression.create') }}
        </button>
      </div>
    </div>

    <p v-if="error" class="note note-error mb-4">{{ error }}</p>

    <ConsolePanel
      :title="kind === 'achievements' ? t('admin.progression.achievementsTitle') : t('admin.progression.questsTitle')"
      :note="`${current.length}`"
    >
      <LoadingSkeleton v-if="loading" :rows="5" :columns="[3, 2, 1, 1, 1]" header />

      <p v-else-if="!current.length" class="voice py-6">
        {{ t('admin.progression.empty') }}
      </p>

      <div v-else class="table-bleed overflow-x-auto">
        <table class="ledger">
          <thead>
            <tr>
              <th scope="col">{{ t('admin.progression.colDefinition') }}</th>
              <th scope="col">{{ t('admin.progression.colSeries') }}</th>
              <th scope="col">{{ t('admin.progression.colTarget') }}</th>
              <th scope="col">{{ t('admin.progression.colState') }}</th>
              <th scope="col">{{ t('admin.progression.colUpdated') }}</th>
              <th scope="col"><span class="sr-only">{{ t('common.actions') }}</span></th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="definition in current" :key="definition.id">
              <td>
                <p class="text-xs text-base-content">{{ definition.title }}</p>
                <p class="mt-0.5 font-mono text-2xs text-faint">{{ definition.identifier }}</p>
              </td>
              <td class="text-xs text-dim">
                {{ definition.series_title || definition.series_identifier || '—' }}
              </td>
              <td class="text-xs text-dim">{{ definition.target_count }}</td>
              <td>
                <div class="flex flex-wrap gap-1">
                  <span class="chip" :class="definition.is_enabled ? 'text-success' : 'text-warning'">
                    {{ definition.is_enabled ? t('common.enabled') : t('common.disabled') }}
                  </span>
                  <span v-if="definition.hidden" class="chip">{{ t('admin.progression.hidden') }}</span>
                  <span v-if="definition.is_seed_managed" class="chip">
                    {{ t('admin.progression.seed') }}
                  </span>
                </div>
              </td>
              <td class="text-xs text-dim">{{ relative(definition.updated_at) }}</td>
              <td>
                <div class="flex justify-end gap-1">
                  <button
                    type="button"
                    class="btn btn-ghost btn-xs"
                    :disabled="busy"
                    @click="openEdit(definition)"
                  >
                    {{ t('common.edit') }}
                  </button>
                  <button
                    type="button"
                    class="btn btn-ghost btn-xs"
                    :disabled="busy || definition.is_seed_managed"
                    @click="toggle(definition)"
                  >
                    {{ definition.is_enabled ? t('common.disable') : t('common.enable') }}
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </ConsolePanel>

    <dialog v-if="editorOpen" class="modal modal-open" aria-modal="true" aria-labelledby="definition-title">
      <div class="modal-box max-w-2xl overflow-hidden p-0">
        <div class="bg-base-200/40 px-6 py-5">
          <h2 id="definition-title" class="text-sm text-base-content">
            {{ editing ? t('admin.progression.editTitle') : t('admin.progression.createTitle') }}
          </h2>
          <p class="voice mt-1 text-[12px]">
            {{ kind === 'achievements' ? t('admin.progression.tabs.achievements') : t('admin.progression.tabs.quests') }}
          </p>
        </div>
        <form class="grid max-h-[70vh] gap-4 overflow-y-auto px-6 py-5 sm:grid-cols-2" @submit.prevent="save">
          <TextField
            id="definition-identifier"
            v-model="draft.identifier"
            :disabled="editing !== null"
            :label="t('admin.progression.identifier')"
          />
          <TextField id="definition-title" v-model="draft.title" :label="t('admin.progression.titleField')" />
          <TextField
            id="definition-summary"
            v-model="draft.summary"
            root-class="sm:col-span-2"
            as="textarea"
            :rows="2"
            :label="t('admin.progression.summary')"
          />
          <TextField id="definition-icon" v-model="draft.icon" :label="t('admin.progression.icon')" />
          <TextField id="definition-sort" v-model="draft.sort_order" type="number" :label="t('admin.progression.sort')" />
          <TextField
            id="definition-series"
            v-model="draft.series_identifier"
            :label="t('admin.progression.seriesIdentifier')"
          />
          <TextField
            id="definition-series-title"
            v-model="draft.series_title"
            :label="t('admin.progression.seriesTitle')"
          />
          <TextField
            id="definition-target"
            v-model="draft.target_count"
            type="number"
            :label="t('admin.progression.target')"
          />
          <TextField
            id="definition-series-order"
            v-model="draft.series_order"
            type="number"
            :label="t('admin.progression.seriesOrder')"
          />
          <TextField
            id="definition-available-from"
            v-model="draft.available_from"
            type="datetime-local"
            :label="t('admin.progression.availableFrom')"
          />
          <TextField
            id="definition-available-until"
            v-model="draft.available_until"
            type="datetime-local"
            :label="t('admin.progression.availableUntil')"
          />
          <div class="flex flex-wrap gap-4 text-xs text-dim sm:col-span-2">
            <label class="flex items-center gap-2">
              <input v-model="draft.hidden" class="checkbox checkbox-xs" type="checkbox" />
              {{ t('admin.progression.hidden') }}
            </label>
            <label class="flex items-center gap-2">
              <input
                v-model="draft.is_progress_enabled"
                class="checkbox checkbox-xs"
                type="checkbox"
              />
              {{ t('admin.progression.progressEnabled') }}
            </label>
          </div>
          <TextField
            id="definition-trigger"
            v-model="draft.trigger"
            root-class="sm:col-span-2"
            as="textarea"
            :rows="4"
            :label="t('admin.progression.trigger')"
            :hint="t('admin.progression.jsonHint')"
          />
          <TextField
            v-if="kind === 'quests'"
            id="definition-schedule"
            v-model="draft.schedule"
            root-class="sm:col-span-2"
            as="textarea"
            :rows="3"
            :label="t('admin.progression.schedule')"
            :hint="t('admin.progression.jsonHint')"
          />
          <TextField
            id="definition-reward"
            v-model="draft.reward"
            root-class="sm:col-span-2"
            as="textarea"
            :rows="4"
            :label="t('admin.progression.reward')"
            :hint="t('admin.progression.jsonHint')"
          />
          <div class="flex items-center justify-end gap-2 sm:col-span-2">
            <button type="button" class="btn btn-ghost btn-xs" @click="editorOpen = false">
              {{ t('common.cancel') }}
            </button>
            <button
              type="submit"
              class="btn btn-primary btn-xs"
              :disabled="busy || !draft.identifier.trim() || !draft.title.trim()"
            >
              {{ t('common.save') }}
            </button>
          </div>
        </form>
      </div>
      <button
        type="button"
        class="modal-backdrop bg-black/60"
        :aria-label="t('common.cancel')"
        @click="editorOpen = false"
      />
    </dialog>
  </div>
</template>
