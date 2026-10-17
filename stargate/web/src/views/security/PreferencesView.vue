<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { useSessionStore } from '@/stores/session'
import { useNoticeStore } from '@/stores/notice'
import { securityApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import { securityModeCopy } from '@/lib/enums'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import SecurityNav from '@/components/SecurityNav.vue'

const { t } = useI18n()
const session = useSessionStore()
const notice = useNoticeStore()

const mode = ref('default')
const loading = ref(true)
const saving = ref(false)
const error = ref('')

const modes = ['default', 'lockdown', 'lockoff']

const modeNames = computed<Record<string, string>>(() => ({
  default: t('preferences.modeDefault'),
  lockdown: t('preferences.modeLockdown'),
  lockoff: t('preferences.modeLockoff'),
}))

const saved = ref('default')
const dirty = computed(() => mode.value !== saved.value)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const preferences = await securityApi.preferences()
    mode.value = preferences.mode
    saved.value = preferences.mode
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('preferences.errorLoad')
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  error.value = ''
  try {
    const preferences = await securityApi.updatePreferences(mode.value)
    saved.value = preferences.mode
    notice.ok(t('preferences.noticeMode', { mode: modeNames.value[preferences.mode] ?? preferences.mode }))
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : t('preferences.errorSave')
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      :title="t('preferences.title')"
      :description="t('preferences.description')"
    />

    <SecurityNav />

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
      <ConsolePanel :title="t('preferences.mode')">
        <div v-if="loading" class="py-6 text-xs text-faint">{{ t('preferences.reading') }}</div>

        <fieldset v-else class="space-y-2">
          <legend class="sr-only">{{ t('preferences.legend') }}</legend>
          <label
            v-for="option in modes"
            :key="option"
            class="flex cursor-pointer items-start gap-3 border border-line px-3 py-3 transition-colors"
            :class="mode === option ? 'border-primary/60 bg-base-300/40' : 'hover:bg-base-300/30'"
          >
            <input
              v-model="mode"
              class="radio radio-xs mt-0.5"
              type="radio"
              name="security-mode"
              :value="option"
            />
            <span class="min-w-0">
              <span class="block text-xs text-base-content">{{ modeNames[option] ?? option }}</span>
              <span class="voice mt-1 block text-[12px] text-faint">
                {{ securityModeCopy(option) }}
              </span>
            </span>
          </label>
        </fieldset>

        <div v-if="!loading" class="mt-5 flex items-center gap-2">
          <button
            type="button"
            class="btn btn-primary btn-sm"
            :disabled="saving || !dirty"
            @click="save"
          >
            {{ saving ? t('common.saving') : t('preferences.saveMode') }}
          </button>
          <span v-if="dirty" class="text-2xs text-warning">{{ t('preferences.unsaved') }}</span>
        </div>
      </ConsolePanel>

      <ConsolePanel :title="t('preferences.currentSession')">
        <dl class="space-y-3 text-xs">
          <div class="flex items-baseline justify-between">
            <dt class="text-faint">{{ t('preferences.signedInAs') }}</dt>
            <dd class="text-dim">{{ session.identity?.name }}</dd>
          </div>
          <div class="flex items-baseline justify-between">
            <dt class="text-faint">{{ t('preferences.elevated') }}</dt>
            <dd :class="session.elevated ? 'text-accent' : 'text-dim'">
              {{ session.elevated ? t('common.yes') : t('common.no') }}
            </dd>
          </div>
          <div class="flex items-baseline justify-between">
            <dt class="text-faint">{{ t('preferences.tokenType') }}</dt>
            <dd class="text-dim">{{ session.identity?.token_type }}</dd>
          </div>
        </dl>
        <p class="voice mt-4 text-[12px] text-faint">
          {{ t('preferences.lockdownHint') }}
        </p>
      </ConsolePanel>
    </div>
  </div>
</template>
