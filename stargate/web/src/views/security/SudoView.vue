<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'

import PageHeader from '@/components/PageHeader.vue'
import SudoFlow from '@/components/SudoFlow.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const done = ref(false)

/** Only same-origin paths are accepted as a return target. */
const nextPath = computed(() => {
  const raw = route.query.next
  return typeof raw === 'string' && raw.startsWith('/') && !raw.startsWith('//') ? raw : null
})

const inPopup = computed(() => typeof window !== 'undefined' && window.opener !== null)

function onDone() {
  done.value = true
  // The app that opened this page can react without polling; the message
  // carries no session data.
  window.opener?.postMessage({ type: 'solarpass:sudo', elevated: true }, '*')
}

function finish() {
  if (inPopup.value) {
    window.close()
    return
  }
  void router.replace(nextPath.value ?? '/account')
}
</script>

<template>
  <div>
    <PageHeader :title="t('sudo.title')" :description="t('sudo.description')" />

    <div v-if="done" class="bg-base-200/30 p-5">
      <p class="text-sm text-base-content">{{ t('sudo.verified') }}</p>
      <p class="voice mt-2">{{ t('sudo.verifiedHint') }}</p>
      <button type="button" class="btn btn-primary btn-sm mt-4" @click="finish">
        {{ inPopup ? t('sudo.close') : t('sudo.continue') }}
      </button>
    </div>

    <div v-else class="bg-base-200/30 p-5">
      <SudoFlow @done="onDone" />
      <button
        type="button"
        class="mt-5 text-2xs text-dim hover:text-base-content"
        @click="finish"
      >
        {{ t('common.cancel') }}
      </button>
    </div>
  </div>
</template>
