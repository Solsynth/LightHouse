<script setup lang="ts">
import { onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import QRCode from 'qrcode'
import { QrCode16, Renew16 } from '@carbon/icons-vue'

import { signInApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import { deviceIdentity } from '@/lib/device'

/**
 * One QR sign-in: this browser starts a challenge, a signed-in device scans and
 * approves it, and this page only watches. The code is rendered locally from
 * `qr_data` so nothing about the challenge leaves for a third party to see, and
 * it stops watching the moment it settles — an approved challenge can only be
 * exchanged once.
 */
const emit = defineEmits<{
  /** The phone approved the sign-in; the auth challenge id is ready to redeem. */
  approved: [challengeId: string]
  /** Left the QR step, so the parent may resume the account-first flow. */
  back: []
}>()

const { t } = useI18n()

const POLL_MS = 2000
const DEFAULT_TTL_SECONDS = 300

const image = ref('')
const status = ref<'waiting' | 'loading' | 'scanned' | 'declined' | 'expired'>('loading')
const busy = ref(false)
const error = ref('')
const countdown = ref('')

let qrId = ''
let deadline = 0
let pollTimer: number | undefined
let tickTimer: number | undefined
let settled = false
// Bumped by every start, so a request that lands after the person asked for a
// new code cannot arm timers for the challenge they already replaced.
let run = 0

function stopWatching() {
  if (pollTimer !== undefined) window.clearInterval(pollTimer)
  if (tickTimer !== undefined) window.clearInterval(tickTimer)
  pollTimer = undefined
  tickTimer = undefined
}

function renderCountdown() {
  const left = Math.max(0, Math.floor((deadline - Date.now()) / 1000))
  countdown.value = `${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}`
}

async function tick() {
  renderCountdown()
  if (Date.now() >= deadline && (status.value === 'waiting' || status.value === 'scanned')) {
    status.value = 'expired'
    stopWatching()
  }
}

async function poll() {
  if (!qrId || settled) return
  const attempt = run
  try {
    const next = await signInApi.qrStatus(qrId)
    // A response for a code the person already replaced must not touch the
    // panel that replaced it.
    if (settled || attempt !== run) return
    switch (next.status) {
      case 1:
        status.value = 'scanned'
        break
      case 2:
        // Stop watching before handing the challenge over: a second poll would
        // try to redeem a code the parent is already exchanging.
        settled = true
        status.value = 'scanned'
        stopWatching()
        emit('approved', next.auth_challenge_id)
        break
      case 3:
        settled = true
        status.value = 'declined'
        stopWatching()
        break
      default:
        status.value = 'waiting'
    }
    if (next.expires_at) {
      deadline = new Date(next.expires_at).getTime()
      renderCountdown()
    }
  } catch (caught) {
    // Only a missing code is terminal — Redis keeps a challenge for five
    // minutes, so after that the poll 404s forever. Any other failure (a
    // dropped request, a restarting server) is transient: the loop keeps
    // running so the person does not have to press New Code for a blip.
    if (settled || attempt !== run) return
    if (caught instanceof StargateError && caught.status === 404) {
      settled = true
      status.value = 'expired'
      stopWatching()
    }
  }
}

async function start() {
  stopWatching()
  settled = false
  run += 1
  const attempt = run
  qrId = ''
  image.value = ''
  error.value = ''
  countdown.value = ''
  status.value = 'loading'
  busy.value = true
  try {
    const device = deviceIdentity()
    const created = await signInApi.qrGenerate({
      device_id: device.id,
      device_name: device.name,
      platform: 1,
    })
    if (attempt !== run) return
    qrId = created.qr_challenge_id
    // A QR is read by a camera, not a theme: the tile keeps a light field and
    // dark modules in both themes so any scanner can read it.
    const drawn = await QRCode.toDataURL(created.qr_data, {
      margin: 1,
      width: 200,
      errorCorrectionLevel: 'M',
      color: { dark: '#0a0d16', light: '#ffffff' },
    })
    if (attempt !== run) return
    image.value = drawn
    deadline = created.expires_at
      ? new Date(created.expires_at).getTime()
      : Date.now() + (created.expires_in_seconds || DEFAULT_TTL_SECONDS) * 1000
    status.value = 'waiting'
    renderCountdown()
    pollTimer = window.setInterval(() => void poll(), POLL_MS)
    tickTimer = window.setInterval(() => void tick(), 1000)
    await poll()
  } catch (caught) {
    // A failed start leaves nothing to watch: no code was issued, so drop any
    // timer this attempt had already armed before reporting it.
    settled = true
    stopWatching()
    status.value = 'expired'
    error.value = caught instanceof Error ? caught.message : t('qrLogin.failed')
  } finally {
    busy.value = false
  }
}

function back() {
  stopWatching()
  settled = true
  run += 1
  emit('back')
}

onBeforeUnmount(stopWatching)

void start()
</script>

<template>
  <div class="flex flex-col items-center">
    <h2 class="title text-sm text-base-content">{{ t('qrLogin.title') }}</h2>
    <p class="voice mt-1 text-center text-[12px]">{{ t('qrLogin.intro') }}</p>

    <div
      class="mt-4 flex h-[200px] w-[200px] items-center justify-center border border-line bg-white"
    >
      <img
        v-if="image && status !== 'declined' && status !== 'expired'"
        :src="image"
        class="h-full w-full"
        :alt="t('qrLogin.alt')"
      />
      <div v-else class="flex flex-col items-center gap-2 text-faint">
        <QrCode16 aria-hidden="true" />
        <span class="px-3 text-center text-2xs">
          {{ status === 'declined' ? t('qrLogin.declined') : t('qrLogin.expired') }}
        </span>
      </div>
    </div>

    <p
      class="mt-3 flex items-center gap-2 text-2xs"
      :class="status === 'scanned' ? 'text-primary' : status === 'waiting' ? 'text-faint' : 'text-error'"
    >
      <span v-if="status === 'waiting'" class="dot animate-pulse bg-info" aria-hidden="true" />
      <template v-if="status === 'loading'">{{ t('common.loading') }}</template>
      <template v-else-if="status === 'waiting'">{{ t('qrLogin.waiting') }}</template>
      <template v-else-if="status === 'scanned'">{{ t('qrLogin.scanned') }}</template>
      <template v-else-if="status === 'declined'">{{ t('qrLogin.declined') }}</template>
      <template v-else>{{ t('qrLogin.expired') }}</template>
      <template v-if="countdown && (status === 'waiting' || status === 'scanned')">
        · {{ countdown }}
      </template>
    </p>

    <p v-if="error" class="mt-2 text-center text-2xs text-error">{{ error }}</p>

    <div class="mt-4 flex w-full items-center gap-2">
      <button type="button" class="btn btn-ghost flex-1" @click="back">
        {{ t('common.back') }}
      </button>
      <button type="button" class="btn btn-outline flex-1" :disabled="busy" @click="start">
        <Renew16 aria-hidden="true" />
        {{ t('qrLogin.refresh') }}
      </button>
    </div>
  </div>
</template>
