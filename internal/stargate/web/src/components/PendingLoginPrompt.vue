<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { authApi } from '@/lib/api'
import type { AuthChallenge } from '@/lib/api/types'
import { deviceIdentity } from '@/lib/device'
import { platformLabel } from '@/lib/enums'
import { useNoticeStore } from '@/stores/notice'
import { useSessionStore } from '@/stores/session'

/**
 * The in-app confirmation prompt for recent sign-ins. A password-first login
 * publishes its review prompt only after the password verifies, so this sheet
 * is the place another signed-in device can approve the session or deny it —
 * denying signs that session out. It polls rather than listening on the ws
 * bus because the console holds no socket.
 */
const { t } = useI18n()
const session = useSessionStore()
const notice = useNoticeStore()

const POLL_MS = 15_000
const ownDeviceId = deviceIdentity().id

const challenges = ref<AuthChallenge[]>([])
const dismissed = ref<Set<string>>(new Set())
const busy = ref(false)

let timer: number | undefined
let loading = false

/** The oldest unreviewed challenge that did not start in this browser. */
const current = computed(
  () =>
    challenges.value.find(
      (challenge) =>
        !dismissed.value.has(challenge.id) &&
        challenge.device_id !== ownDeviceId &&
        !challenge.approved_at &&
        !challenge.declined_at,
    ) ?? null,
)

/** A satisfied challenge is a completed sign-in under review; otherwise the
 *  device is still being asked for another factor. */
const finished = computed(() => (current.value?.step_remain ?? 1) === 0)
const device = computed(() => current.value?.device_name || t('common.unknown'))
const place = computed(() => current.value?.ip_address || t('common.unknown'))
const platform = computed(() => (current.value ? platformLabel(current.value.platform) : ''))

async function load() {
  if (!session.signedIn || loading || document.visibilityState === 'hidden') return
  loading = true
  try {
    challenges.value = await authApi.pendingChallenges()
  } catch {
    // A list that cannot be read shows no prompt: polling must never nag.
  } finally {
    loading = false
  }
}

function dismiss() {
  if (!current.value) return
  dismissed.value = new Set(dismissed.value).add(current.value.id)
}

async function decide(approve: boolean) {
  const challenge = current.value
  if (!challenge) return
  busy.value = true
  try {
    if (approve) await authApi.approveChallenge(challenge.id)
    else await authApi.declineChallenge(challenge.id)
    challenges.value = challenges.value.filter((item) => item.id !== challenge.id)
    notice.ok(t(approve ? 'loginPrompt.approved' : 'loginPrompt.denied'))
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('loginPrompt.failed'))
  } finally {
    busy.value = false
  }
}

function onVisibilityChange() {
  void load()
}

onMounted(() => {
  void load()
  timer = window.setInterval(() => void load(), POLL_MS)
  document.addEventListener('visibilitychange', onVisibilityChange)
})
onBeforeUnmount(() => {
  if (timer !== undefined) window.clearInterval(timer)
  document.removeEventListener('visibilitychange', onVisibilityChange)
})
watch(
  () => session.signedIn,
  (signedIn) => {
    if (signedIn) void load()
    else challenges.value = []
  },
)
</script>

<template>
  <dialog
    v-if="current"
    class="modal modal-open"
    aria-modal="true"
    :aria-label="t(finished ? 'loginPrompt.confirmTitle' : 'loginPrompt.title')"
  >
    <div class="modal-box max-w-sm overflow-hidden p-0">
      <div class="px-6 py-5">
        <h2 class="text-sm text-base-content">
          {{ t(finished ? 'loginPrompt.confirmTitle' : 'loginPrompt.title') }}
        </h2>
        <p class="voice mt-2">
          {{
            t(finished ? 'loginPrompt.confirmBody' : 'loginPrompt.body', {
              device,
              ip: place,
            })
          }}
        </p>
        <p class="mt-2 text-2xs text-faint">{{ platform }}</p>
      </div>
      <footer class="flex items-center justify-end gap-2 bg-base-200/50 px-5 py-3">
        <button type="button" class="btn btn-ghost btn-xs" :disabled="busy" @click="dismiss">
          {{ t('common.dismiss') }}
        </button>
        <button type="button" class="btn btn-error btn-xs" :disabled="busy" @click="decide(false)">
          {{ t('loginPrompt.deny') }}
        </button>
        <button
          type="button"
          class="btn btn-primary btn-xs"
          :disabled="busy"
          @click="decide(true)"
        >
          {{ t('loginPrompt.approve') }}
        </button>
      </footer>
    </div>
  </dialog>
</template>
