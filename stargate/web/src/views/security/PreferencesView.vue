<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'

import { useSessionStore } from '@/stores/session'
import { useNoticeStore } from '@/stores/notice'
import { securityApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import { securityModeCopy } from '@/lib/enums'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import SecurityNav from '@/components/SecurityNav.vue'

const session = useSessionStore()
const notice = useNoticeStore()

const mode = ref('default')
const loading = ref(true)
const saving = ref(false)
const error = ref('')

const modes = ['default', 'lockdown', 'lockoff']

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
      caught instanceof StargateError ? caught.message : 'Preferences could not be loaded.'
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
    notice.ok(`Security Mode set to ${preferences.mode}.`)
  } catch (caught) {
    error.value =
      caught instanceof StargateError ? caught.message : 'The mode could not be changed.'
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      title="Security Mode"
      description="How much proof a sign-in needs. The default reads the risk of each attempt; the other two override it."
    />

    <SecurityNav />

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
      <ConsolePanel title="Mode">
        <div v-if="loading" class="py-6 text-xs text-faint">Reading preference…</div>

        <fieldset v-else class="space-y-2">
          <legend class="sr-only">Security Mode</legend>
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
              <span class="block text-xs text-base-content">{{ option }}</span>
              <span class="voice mt-1 block text-[12px] text-faint">
                {{ securityModeCopy[option] }}
              </span>
            </span>
          </label>
        </fieldset>

        <div v-if="!loading" class="mt-5 flex items-center gap-2 border-t border-line-soft pt-4">
          <button
            type="button"
            class="btn btn-primary btn-sm"
            :disabled="saving || !dirty"
            @click="save"
          >
            {{ saving ? 'Saving…' : 'Save mode' }}
          </button>
          <span v-if="dirty" class="text-2xs text-warning">unsaved change</span>
        </div>
      </ConsolePanel>

      <ConsolePanel title="Current Session">
        <dl class="space-y-3 text-xs">
          <div class="flex items-baseline justify-between">
            <dt class="text-faint">signed in as</dt>
            <dd class="text-dim">{{ session.identity?.name }}</dd>
          </div>
          <div class="flex items-baseline justify-between">
            <dt class="text-faint">elevated</dt>
            <dd :class="session.elevated ? 'text-accent' : 'text-dim'">
              {{ session.elevated ? 'yes' : 'no' }}
            </dd>
          </div>
          <div class="flex items-baseline justify-between">
            <dt class="text-faint">token type</dt>
            <dd class="text-dim">{{ session.identity?.token_type }}</dd>
          </div>
        </dl>
        <p class="voice mt-4 text-[12px] text-faint">
          Lockdown is strict: a lost factor locks the account out until an administrator clears it.
        </p>
      </ConsolePanel>
    </div>
  </div>
</template>
