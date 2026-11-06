<script setup lang="ts">
import { onBeforeUnmount, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'

import { useSessionStore } from '@/stores/session'
import { useElevationStore } from '@/stores/elevation'
import SudoFlow from '@/components/SudoFlow.vue'

const { t } = useI18n()
const session = useSessionStore()
const elevation = useElevationStore()

function dismiss() {
  session.dismissElevation()
  elevation.reset()
}

function done() {
  elevation.reset()
}

function onKey(event: KeyboardEvent) {
  if (event.key === 'Escape' && session.elevationOpen) dismiss()
}

onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <!-- Both sheets are daisyUI modals at z-999, so DOM order would otherwise
       put the pending-login prompt (rendered after this) over the elevation
       sheet and make sudo unusable while a login is awaiting approval. -->
  <dialog
    v-if="session.elevationOpen"
    class="modal modal-open z-[1000]"
    aria-labelledby="elevation-title"
    aria-modal="true"
  >
    <div class="modal-box max-w-md overflow-hidden p-0">
      <header class="bg-base-200/40 px-6 py-5">
        <p class="eyebrow">{{ t('elevation.required') }}</p>
        <h2 id="elevation-title" class="mt-1 text-sm text-base-content">
          {{ t('elevation.title') }}
        </h2>
        <p class="voice mt-2">
          {{ t('elevation.description') }}
        </p>
      </header>

      <div class="px-6 py-5">
        <SudoFlow @done="done" />
      </div>

      <footer class="flex items-center justify-between bg-base-200/50 px-5 py-3">
        <span class="text-2xs text-faint">{{ t('elevation.footer') }}</span>
        <button type="button" class="btn btn-ghost btn-xs" @click="dismiss">
          {{ t('common.cancel') }}
        </button>
      </footer>
    </div>
    <button
      type="button"
      class="modal-backdrop bg-black/60"
      :aria-label="t('elevation.cancelElevation')"
      @click="dismiss"
    />
  </dialog>
</template>
