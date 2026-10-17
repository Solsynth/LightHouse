<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Unlink16, View16, ViewOff16 } from '@carbon/icons-vue'

import { useNoticeStore } from '@/stores/notice'
import { appApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { Connection } from '@/lib/api/types'
import { relative } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import IconButton from '@/components/IconButton.vue'
import SecurityNav from '@/components/SecurityNav.vue'

const { t } = useI18n()
const notice = useNoticeStore()

const connections = ref<Connection[]>([])
const loading = ref(true)
const error = ref('')
const busy = ref(false)

async function load() {
  loading.value = true
  error.value = ''
  try {
    connections.value = (await appApi.connections()) ?? []
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('connections.errorLoad')
  } finally {
    loading.value = false
  }
}

async function toggleVisibility(connection: Connection) {
  busy.value = true
  try {
    await appApi.setConnectionVisibility(connection.id, !connection.is_public)
    notice.ok(
      connection.is_public
        ? t('connections.noticeHidden')
        : t('connections.noticeShown'),
    )
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('connections.errorVisibility'))
  } finally {
    busy.value = false
  }
}

async function removeConnection(connection: Connection) {
  busy.value = true
  try {
    await appApi.removeConnection(connection.id)
    notice.ok(t('connections.noticeRemoved'))
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('connections.errorRemove'))
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader :title="t('connections.title')" :description="t('connections.description')" />

    <SecurityNav />

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <ConsolePanel
      :title="t('connections.panelTitle')"
      :note="loading ? undefined : t('connections.note', { n: connections.length })"
    >
      <LoadingSkeleton v-if="loading" variant="rows" :rows="3" />
      <p v-else-if="!connections.length" class="voice py-4 text-[12px]">
        {{ t('connections.empty') }}
      </p>

      <ul v-else>
        <li
          v-for="connection in connections"
          :key="connection.id"
          class="flex flex-wrap items-center justify-between gap-3 py-3 first:pt-0 last:pb-0"
        >
          <div>
            <div class="flex flex-wrap items-center gap-2">
              <span class="text-xs text-base-content">{{ connection.provider }}</span>
              <span class="chip">{{ connection.provided_identifier }}</span>
              <span v-if="connection.is_public" class="chip text-accent">
                {{ t('connections.public') }}
              </span>
              <span v-if="connection.registered_at" class="chip text-primary">
                {{ t('connections.createdAccount') }}
              </span>
            </div>
            <p class="mt-1 text-2xs text-faint">
              {{ t('connections.lastUsedAt', { time: relative(connection.last_used_at) }) }}
            </p>
          </div>
          <div class="flex items-center gap-1">
            <IconButton
              :label="connection.is_public ? t('connections.hide') : t('connections.showPublicly')"
              :disabled="busy"
              @click="toggleVisibility(connection)"
            >
              <ViewOff16 v-if="connection.is_public" aria-hidden="true" />
              <View16 v-else aria-hidden="true" />
            </IconButton>
            <IconButton
              v-if="!connection.registered_at"
              :label="t('connections.unlink')"
              tone="danger"
              :disabled="busy"
              @click="removeConnection(connection)"
            >
              <Unlink16 aria-hidden="true" />
            </IconButton>
          </div>
        </li>
      </ul>
    </ConsolePanel>
  </div>
</template>
