<script setup lang="ts">
/**
 * The picker's own session gate.
 *
 * The drive's gate is a full page with its own chrome; inside a 560×480 frame
 * that chrome would take the whole surface. More importantly a frame cannot
 * complete a sign-in redirect on its own — the identity host refuses to be
 * framed, and the cookie would land third-party — so the sign-in control
 * leaves the frame and the host is told what happened.
 */
import { computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { CloudOffline16, Login16, Renew16 } from '@carbon/icons-vue'

import { useSessionStore } from '@/stores/session'

const props = defineProps<{ embedded: boolean }>()
const emit = defineEmits<{ 'signed-out': []; ready: [] }>()

const session = useSessionStore()
const { t } = useI18n()

const state = computed(() => session.state)

function signIn() {
  // A centered popup is used to complete authentication instead of an unlinked new tab.
  // The popup communicates back via postMessage to window.opener, enabling the embedded
  // frame to receive the token or session update and close the popup automatically.
  try {
    const url = new URL(session.authUrl)
    url.searchParams.set('popup', '1')
    const w = 520
    const h = 680
    const left = window.screenX + (window.outerWidth - w) / 2
    const top = window.screenY + (window.outerHeight - h) / 2
    window.open(
      url.toString(),
      'SolianAuth',
      `width=${w},height=${h},top=${top},left=${left},status=no,menubar=no,toolbar=no`,
    )
  } catch {
    window.open(session.authUrl, '_blank')
  }
  emit('signed-out')
}

watch(
  () => state.value,
  (next) => {
    if (next === 'signed-out') emit('signed-out')
    if (next === 'ready') emit('ready')
  },
  { immediate: true },
)
</script>

<template>
  <slot v-if="state === 'ready'" />

  <div class="flex h-dvh flex-col items-center justify-center bg-base-100 p-3 text-center">
    <div class="w-full max-w-[340px] rounded-sheet bg-base-200 px-7 py-8">
      <template v-if="state === 'loading'">
        <div class="loading-scan mx-auto h-[2px] w-32 bg-base-300/50" />
        <p class="mt-4 text-2xs text-faint">{{ t('session.connecting') }}</p>
      </template>

      <template v-else-if="state === 'signed-out'">
        <span class="mx-auto grid size-11 place-items-center rounded-card bg-base-300/50">
          <Login16 class="h-5 w-5 text-primary" aria-hidden="true" />
        </span>
        <h1 class="title mt-4 text-[17px]">{{ t('session.signInRequired') }}</h1>
        <p class="voice mt-2 text-[12px]">{{ t('session.signInBody') }}</p>
        <button type="button" class="btn btn-primary btn-sm mt-4" @click="signIn">
          {{ t('session.signIn') }}
        </button>
        <p v-if="props.embedded" class="mt-3 text-2xs text-faint">Opens in a popup window.</p>
      </template>

      <template v-else>
        <span class="mx-auto grid size-11 place-items-center rounded-card bg-error/12">
          <CloudOffline16 class="h-5 w-5 text-error" aria-hidden="true" />
        </span>
        <h1 class="title mt-4 text-[17px]">{{ t('error.storageUnreachable') }}</h1>
        <p
          v-if="session.error"
          class="mt-2 break-all rounded-chip bg-base-300/40 px-3 py-2 font-mono text-2xs text-error"
        >
          {{ session.error }}
        </p>
        <button type="button" class="btn btn-outline btn-sm mt-4" @click="session.retry()">
          <Renew16 />
          {{ t('session.tryAgain') }}
        </button>
      </template>
    </div>
  </div>
</template>
