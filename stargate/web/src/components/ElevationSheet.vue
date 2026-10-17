<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

import { useSessionStore } from '@/stores/session'
import { useElevationStore } from '@/stores/elevation'
import { useNoticeStore } from '@/stores/notice'
import { request } from '@/lib/api/client'
import type { AuthChallenge, AuthFactor } from '@/lib/api/types'
import { codeRequestFactorTypes, factorTypeLabels, pickableFactorTypes } from '@/lib/enums'
import { remaining } from '@/lib/format'

const session = useSessionStore()
const elevation = useElevationStore()
const notice = useNoticeStore()

const factors = ref<AuthFactor[]>([])
const used = ref<string[]>([])
const secrets = ref<Record<string, string>>({})
const codeSentFor = ref<string | null>(null)
const error = ref('')
const busy = ref(false)
const challenge = ref<AuthChallenge | null>(null)

async function begin() {
  busy.value = true
  error.value = ''
  try {
    challenge.value = elevation.state?.challenge ?? (await elevation.begin())
    await loadFactors()
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : 'Elevation could not be started.'
  } finally {
    busy.value = false
  }
}

async function loadFactors() {
  if (!challenge.value) return
  const list = await request<AuthFactor[]>(`/auth/challenge/${challenge.value.id}/factors`)
  const blacklist = new Set(challenge.value.blacklist_factors)
  factors.value = list.filter(
    (factor) =>
      pickableFactorTypes[factor.type] === true &&
      !blacklist.has(factor.id) &&
      !used.value.includes(factor.id),
  )
}

async function sendCode(factor: AuthFactor) {
  if (!challenge.value) return
  error.value = ''
  try {
    await request<void>(`/auth/challenge/${challenge.value.id}/factors/${factor.id}`, {
      method: 'POST',
    })
    codeSentFor.value = factor.id
    notice.ok(`Code sent for ${factorTypeLabels[factor.type] ?? 'factor'}.`)
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : 'The code could not be sent.'
  }
}

async function submit(factor: AuthFactor) {
  const secret = secrets.value[factor.id]
  if (!challenge.value || !secret) return
  busy.value = true
  error.value = ''
  try {
    const updated = await request<AuthChallenge>(`/auth/challenge/${challenge.value.id}`, {
      method: 'PATCH',
      body: { factor_id: factor.id, password: secret },
    })
    challenge.value = updated
    used.value = [...used.value, factor.id]
    secrets.value = {}
    codeSentFor.value = null
    if (updated.step_remain === 0) {
      session.noteElevation(updated)
      notice.ok('Elevation granted for this session.')
      reset()
    } else {
      await loadFactors()
    }
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : 'That credential was not accepted.'
  } finally {
    busy.value = false
  }
}

function reset() {
  factors.value = []
  used.value = []
  secrets.value = {}
  codeSentFor.value = null
  error.value = ''
  challenge.value = null
}

function dismiss() {
  session.dismissElevation()
  elevation.reset()
  reset()
}

function onKey(event: KeyboardEvent) {
  if (event.key === 'Escape' && session.elevationOpen) dismiss()
}

watch(
  () => session.elevationOpen,
  (open) => {
    if (open && !challenge.value) void begin()
  },
  { immediate: true },
)

onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <dialog
    v-if="session.elevationOpen"
    class="modal modal-open"
    aria-labelledby="elevation-title"
    aria-modal="true"
  >
    <div class="modal-box max-w-md rounded-[2px] border border-line bg-base-100 p-0 shadow-none">
      <header class="border-b border-line px-5 py-4">
        <p class="eyebrow">elevation required</p>
        <h2 id="elevation-title" class="mt-1 text-sm text-base-content">Re-Verify to Continue</h2>
        <p class="voice mt-2">
          This action changes account security. Satisfy the factors below; the grant lasts for
          this session only.
        </p>
      </header>

      <div class="px-5 py-4">
        <div v-if="challenge" class="mb-4 flex items-center justify-between text-2xs text-faint">
          <span>
            {{ challenge.step_total - challenge.step_remain }}/{{ challenge.step_total }} steps
            complete
          </span>
          <span>{{ remaining(challenge.expired_at) }} left</span>
        </div>

        <p v-if="busy && !factors.length" class="py-6 text-xs text-faint">Starting challenge…</p>

        <p v-else-if="!factors.length" class="voice py-6">
          Nothing on this account can satisfy this step. Add a Factor in Security, or retry from
          the device that holds your passkey.
        </p>

        <ul v-else class="space-y-4">
          <li
            v-for="factor in factors"
            :key="factor.id"
            class="border-t border-line-soft pt-4 first:border-0 first:pt-0"
          >
            <div class="flex items-baseline justify-between gap-3">
              <label :for="`factor-${factor.id}`" class="text-xs text-base-content">
                {{ factorTypeLabels[factor.type] ?? 'factor' }}
              </label>
              <button
                v-if="codeRequestFactorTypes[factor.type] === true"
                type="button"
                class="text-2xs text-dim underline decoration-line underline-offset-4 hover:text-base-content"
                @click="sendCode(factor)"
              >
                {{ codeSentFor === factor.id ? 'resend code' : 'send code' }}
              </button>
            </div>
            <div class="mt-2 flex gap-2">
              <input
                :id="`factor-${factor.id}`"
                v-model="secrets[factor.id]"
                class="input input-sm flex-1"
                type="password"
                autocomplete="current-password"
                :placeholder="codeRequestFactorTypes[factor.type] === true ? '6-digit code' : 'credential'"
                @keydown.enter="submit(factor)"
              />
              <button
                type="button"
                class="btn btn-primary btn-sm"
                :disabled="!secrets[factor.id] || busy"
                @click="submit(factor)"
              >
                Verify
              </button>
            </div>
          </li>
        </ul>

        <p v-if="error" class="mt-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>
      </div>

      <footer class="flex items-center justify-between border-t border-line px-5 py-3">
        <span class="text-2xs text-faint">Elevation is bound to this browser session.</span>
        <button type="button" class="btn btn-ghost btn-xs" @click="dismiss">Cancel</button>
      </footer>
    </div>
    <button
      type="button"
      class="modal-backdrop bg-black/60"
      aria-label="Cancel elevation"
      @click="dismiss"
    />
  </dialog>
</template>
