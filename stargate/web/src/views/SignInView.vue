<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { FingerprintRecognition16 } from '@carbon/icons-vue'

import { useSignInStore } from '@/stores/signIn'
import { useSessionStore } from '@/stores/session'
import { useNoticeStore } from '@/stores/notice'
import { signInApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AuthFactor } from '@/lib/api/types'
import { codeRequestFactorTypes, factorTypeLabels } from '@/lib/enums'
import { remaining } from '@/lib/format'
import { getAssertion, passkeyFailure, passkeysSupported } from '@/lib/webauthn'
import { deviceIdentity } from '@/lib/device'
import FactorRow from '@/components/FactorRow.vue'

const signIn = useSignInStore()
const session = useSessionStore()
const notice = useNoticeStore()
const router = useRouter()
const route = useRoute()

const account = ref('')
const error = ref('')
const busy = ref(false)
const started = ref(false)
const passkeyFactor = ref<AuthFactor | null>(null)
const codeSentFor = ref<string | null>(null)

let pollTimer: number | undefined

const needsCode = (factor: AuthFactor) => codeRequestFactorTypes[factor.type] === true
const canUsePasskey = computed(() => passkeySupported && passkeyFactor.value !== null)
const passkeySupported = passkeysSupported()

function messageOf(caught: unknown): string {
  if (caught instanceof StargateError) {
    switch (caught.code) {
      case 'AUTH_ACCOUNT_NOT_FOUND':
        return 'No account matches that name or email.'
      case 'NO_AUTH_FACTORS':
        return 'That account has no sign-in method configured.'
      case 'ACCOUNT_LOCKED':
        return caught.detail ?? 'That account is locked.'
      case 'AUTH_SESSION_NOT_TRUSTED':
        return 'Only a trusted session can approve a sign-in.'
      default:
        return caught.message
    }
  }
  return caught instanceof Error ? caught.message : 'The request failed.'
}

async function identify() {
  if (!account.value.trim()) return
  busy.value = true
  error.value = ''
  try {
    await signIn.start(account.value.trim())
    started.value = true
    passkeyFactor.value = signIn.factors.find((factor) => factor.type === 7) ?? null
    if (signIn.complete) await finish()
  } catch (caught) {
    error.value = messageOf(caught)
  } finally {
    busy.value = false
  }
}

/** Account-less passkey sign-in: the authenticator picks the credential. */
async function passkeyDiscoverable() {
  busy.value = true
  error.value = ''
  try {
    const device = deviceIdentity()
    const start = await signInApi.passkeyDiscoverableStart({
      device_id: device.id,
      device_name: device.name,
      platform: 1,
    })
    const assertion = await getAssertion({
      challenge: start.challenge,
      rpId: start.rp_id,
      allowCredentials: start.allow_credentials,
      timeout: start.timeout,
      userVerification: start.user_verification,
    })
    await signInApi.passkeyDiscoverableComplete(start.auth_challenge_id, assertion)
    await signInApi.exchange(start.auth_challenge_id)
    await session.load()
    notice.ok('Signed in with a passkey.')
    await router.replace(nextPath())
  } catch (caught) {
    error.value = caught instanceof StargateError ? messageOf(caught) : passkeyFailure(caught)
  } finally {
    busy.value = false
  }
}

/** Challenge-bound passkey: the server names the registered credentials. */
async function passkeyBound() {
  if (!signIn.challenge || !passkeyFactor.value) return
  busy.value = true
  error.value = ''
  try {
    const start = await signInApi.passkeyStart(signIn.challenge.id)
    const assertion = await getAssertion({
      challenge: start.challenge,
      rpId: start.rp_id,
      allowCredentials: start.allow_credentials,
      timeout: start.timeout,
      userVerification: start.user_verification,
    })
    await signInApi.passkeyComplete(signIn.challenge.id, assertion)
    await finish()
  } catch (caught) {
    error.value = caught instanceof StargateError ? messageOf(caught) : passkeyFailure(caught)
  } finally {
    busy.value = false
  }
}

async function sendCode(factor: AuthFactor) {
  error.value = ''
  try {
    await signIn.requestCode(factor.id)
    codeSentFor.value = factor.id
    notice.ok(`Code sent for ${factorTypeLabels[factor.type]}.`)
  } catch (caught) {
    error.value = messageOf(caught)
  }
}

async function submit(factor: AuthFactor, secret: string) {
  try {
    await signIn.submitFactor(factor.id, secret)
    codeSentFor.value = null
    if (signIn.complete) await finish()
  } catch (caught) {
    error.value = messageOf(caught)
  }
}

function nextPath(): string {
  return typeof route.query.next === 'string' ? route.query.next : '/account'
}

async function finish() {
  busy.value = true
  try {
    await signIn.exchange()
    await session.load()
    signIn.reset()
    notice.ok('Signed in.')
    await router.replace(nextPath())
  } catch (caught) {
    error.value = messageOf(caught)
  } finally {
    busy.value = false
  }
}

/** Cross-device approval completes the challenge server-side; poll for it. */
function startPolling() {
  pollTimer = window.setInterval(async () => {
    if (!signIn.challenge || signIn.complete) return
    try {
      await signIn.poll()
      if (signIn.complete) {
        await finish()
      }
    } catch {
      // Transient poll failures should not interrupt the sign-in.
    }
  }, 4000)
}

function restart() {
  if (pollTimer !== undefined) window.clearInterval(pollTimer)
  signIn.reset()
  started.value = false
  passkeyFactor.value = null
  error.value = ''
  startPolling()
}

onMounted(startPolling)
onBeforeUnmount(() => {
  if (pollTimer !== undefined) window.clearInterval(pollTimer)
})
</script>

<template>
  <div>
    <div class="mb-8">
      <h1 class="title mt-2 text-2xl text-base-content">
        {{ started ? 'Continue Sign-In' : 'Sign In' }}
      </h1>
      <p class="voice mt-2">
        {{
          started
            ? 'Satisfy the factors this sign-in asks for. Sessions are issued as HttpOnly cookies.'
            : 'Use your Solar Network account. Sessions are issued as HttpOnly cookies.'
        }}
      </p>
    </div>

    <Transition name="step" mode="out-in">
    <form v-if="!started" key="identify" class="border border-line bg-base-200/40 p-5" @submit.prevent="identify">
      <label for="account" class="field-label">Account or email</label>
      <input
        id="account"
        v-model="account"
        class="input"
        autocomplete="username"
        autocapitalize="off"
        spellcheck="false"
        placeholder="name or name@example.com"
        :disabled="busy"
      />
      <button type="submit" class="btn btn-primary mt-4 w-full" :disabled="busy || !account.trim()">
        {{ busy ? 'Checking…' : 'Continue' }}
      </button>

      <div v-if="passkeySupported" class="mt-4 border-t border-line-soft pt-4">
        <button
          type="button"
          class="btn btn-outline w-full"
          :disabled="busy"
          @click="passkeyDiscoverable"
        >
          <FingerprintRecognition16 aria-hidden="true" />
          Sign In with a Passkey
        </button>
        <p class="mt-2 text-2xs text-faint">
          Skips the account field — the browser offers the passkeys saved for this site.
        </p>
      </div>
    </form>

    <div v-else key="challenge" class="border border-line bg-base-200/40">
      <header class="flex items-center justify-between border-b border-line px-5 py-3">
        <div class="min-w-0">
          <p class="truncate text-xs text-base-content">{{ signIn.account }}</p>
          <p class="text-2xs text-faint">
            {{ signIn.challenge?.step_remain }} of {{ signIn.challenge?.step_total }} steps
            remaining
            <template v-if="signIn.challenge?.expired_at">
              · {{ remaining(signIn.challenge.expired_at) }} left
            </template>
          </p>
        </div>
        <button type="button" class="text-2xs text-dim hover:text-base-content" @click="restart">
          Change Account
        </button>
      </header>

      <div class="px-5 py-4">
        <button
          v-if="canUsePasskey"
          type="button"
          class="btn btn-outline mb-4 w-full"
          :disabled="busy"
          @click="passkeyBound"
        >
          <FingerprintRecognition16 aria-hidden="true" />
          Verify with a Passkey
        </button>

        <p v-if="!signIn.factors.length" class="py-3 text-xs text-dim">
          Waiting for approval from another device…
        </p>

        <TransitionGroup name="row" tag="div" class="relative">
          <FactorRow
            v-for="factor in signIn.factors.filter((item) => item.type !== 7)"
            :key="factor.id"
            :factor="factor"
            :busy="busy"
            :code-sent="codeSentFor === factor.id"
            :needs-code="needsCode(factor)"
            @send-code="sendCode(factor)"
            @submit="(secret) => submit(factor, secret)"
          />
        </TransitionGroup>
      </div>

      <p v-if="error || signIn.error" class="border-t border-line px-5 py-3 text-xs text-error">
        {{ error || signIn.error }}
      </p>
    </div>
    </Transition>

    <div v-if="started" class="mt-4 flex items-center gap-2 text-2xs text-faint">
      <span class="dot animate-pulse bg-info" aria-hidden="true" />
      <span>Watching for approval from a trusted device.</span>
    </div>

    <p class="mt-8 voice text-[12px] text-faint">
      No account yet?
      <RouterLink to="/create-account" class="link-quiet">Create one</RouterLink>.
      Lost your password?
      <RouterLink to="/recover" class="link-quiet">Reset it by email</RouterLink>.
    </p>
  </div>
</template>
