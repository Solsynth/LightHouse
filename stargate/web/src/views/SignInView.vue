<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { FingerprintRecognition16, QrCode16 } from '@carbon/icons-vue'

import { useSignInStore } from '@/stores/signIn'
import { useSessionStore } from '@/stores/session'
import { useNoticeStore } from '@/stores/notice'
import { signInApi, authApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AuthFactor } from '@/lib/api/types'
import { codeRequestFactorTypes, factorTypeDescription, factorTypeLabel } from '@/lib/enums'
import { remaining } from '@/lib/format'
import { getAssertion, passkeyFailure, passkeysSupported } from '@/lib/webauthn'
import { deviceIdentity } from '@/lib/device'
import { MODULES } from '@/lib/modules'
import { useModulesStore } from '@/stores/modules'
import { landingQuery, requestedLanding, resolveLanding, type Landing } from '@/lib/signInTarget'
import ProviderMark from '@/components/ProviderMark.vue'
import { providerLabel } from '@/lib/providers'
import FactorRow from '@/components/FactorRow.vue'
import QrLoginPanel from '@/components/QrLoginPanel.vue'

const { t } = useI18n()
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
const codeSentIds = ref<string[]>([])
const selectedFactorId = ref<string | null>(null)
const providers = ref<string[]>([])
const mode = ref<'identify' | 'qr'>('identify')
const modules = useModulesStore()

/**
 * Where this sign-in ends. The address may name another Solar Network app
 * (`?redirectUri=`, for a shared login page) or a page of this console
 * (`?next=`); a target the server's hand-off policy refuses is dropped, so the
 * sign-in still completes on the account page.
 */
const landing = ref<Landing | null>(null)
const landingRequested = requestedLanding(route.query)
// A provider tile is a plain link, so its return URL has to be final before it
// is clickable. Until the policy answers, the tile renders without an address.
const landingPending = ref(landingRequested !== '')
const landingResolved = resolveLanding(landingRequested).then((resolved) => {
  landing.value = resolved
  landingPending.value = false
  if (landingRequested && resolved === null) notice.fail(t('signIn.errorRedirectRefused'))
  return resolved ?? { target: '/account', external: false }
})

/** The return URL a provider round-trip carries; it must be resolved first. */
const providerReturnUrl = computed(() => landing.value?.target ?? '/account')

/** Social sign-in is a module surface: an unmounted one lists no providers. */
const socialServed = computed(() => modules.hasAll([MODULES.social]))

let pollTimer: number | undefined

/**
 * Full-page navigation: the server stores the state and redirects onward. The
 * device id rides along so the session the provider creates is bound to this
 * browser rather than an anonymous device row.
 */
function providerUrl(provider: string): string {
  const query = new URLSearchParams({
    flow: 'login',
    returnUrl: providerReturnUrl.value,
    deviceId: deviceIdentity().id,
  })
  return `/api/auth/login/${encodeURIComponent(provider)}?${query.toString()}`
}
async function loadProviders() {
  if (!socialServed.value) {
    providers.value = []
    return
  }
  try {
    providers.value = await authApi.providers()
  } catch {
    // An older server has no provider listing; hide the section.
    providers.value = []
  }
}

/** A QR sign-in is approved on a phone, so it needs no account or factor. */
async function qrApproved(challengeId: string) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    await signInApi.exchange(challengeId)
    await session.load()
    signIn.reset()
    notice.ok(t('signIn.noticeSignedIn'))
    await leave()
  } catch (caught) {
    error.value = messageOf(caught)
    mode.value = 'identify'
  } finally {
    busy.value = false
  }
}

const needsCode = (factor: AuthFactor) => codeRequestFactorTypes[factor.type] === true
const canUsePasskey = computed(() => passkeySupported && passkeyFactor.value !== null)
const passkeySupported = passkeysSupported()

/**
 * QR sign-in is served by the auth module's challenge surface. An unloaded
 * module read fails open, matching `hasAll`: a transient failure must not hide
 * a way the deployment actually offers.
 */
const qrServed = computed(
  () => !modules.loaded || modules.capabilities.includes('auth.qr-login'),
)

/**
 * Passkey and QR are this console's own ways in: they sit together in one row
 * of labelled tiles. Providers are third-party identities and get a row of
 * brand marks only — the mark is the name, and the accessible label spells it
 * out for a screen reader.
 */
const deviceWays = computed(() => {
  const ways: string[] = []
  if (passkeySupported) ways.push('passkey')
  if (qrServed.value) ways.push('qr')
  return ways
})

/** Passkeys have their own assertion endpoint, so the list only offers
 *  factors that submit a secret through the challenge. */
const choiceFactors = computed(() => signIn.factors.filter((factor) => factor.type !== 7))

const selectedFactor = computed(
  () => choiceFactors.value.find((factor) => factor.id === selectedFactorId.value) ?? null,
)

function messageOf(caught: unknown): string {
  if (caught instanceof StargateError) {
    switch (caught.code) {
      case 'AUTH_ACCOUNT_NOT_FOUND':
        return t('signIn.errorAccountNotFound')
      case 'NO_AUTH_FACTORS':
        return t('signIn.errorNoFactors')
      case 'ACCOUNT_LOCKED':
        return caught.detail ?? t('signIn.errorLocked')
      case 'AUTH_SESSION_NOT_TRUSTED':
        return t('signIn.errorUntrusted')
      default:
        return caught.message
    }
  }
  return caught instanceof Error ? caught.message : t('signIn.errorRequestFailed')
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
    else await autoSelect()
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
    notice.ok(t('signIn.noticePasskey'))
    await leave()
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

/** Picks the factor the person will satisfy, and issues a code when the
 *  factor needs one. Selecting is explicit so a code is never sent for a
 *  method the person did not choose. */
async function chooseFactor(factor: AuthFactor) {
  selectedFactorId.value = factor.id
  if (needsCode(factor) && !codeSentIds.value.includes(factor.id)) await sendCode(factor)
}

/** With one usable factor there is nothing to choose; go straight to it. */
async function autoSelect() {
  if (selectedFactor.value) return
  const only = choiceFactors.value[0]
  if (choiceFactors.value.length === 1 && only) await chooseFactor(only)
}

async function sendCode(factor: AuthFactor | null) {
  if (!factor) return
  error.value = ''
  try {
    await signIn.requestCode(factor.id)
    codeSentIds.value = [...codeSentIds.value, factor.id]
    notice.ok(t('signIn.noticeCodeSent', { factor: factorTypeLabel(factor.type) }))
  } catch (caught) {
    error.value = messageOf(caught)
  }
}

async function submit(factor: AuthFactor | null, secret: string) {
  if (!factor) return
  try {
    await signIn.submitFactor(factor.id, secret)
    codeSentIds.value = codeSentIds.value.filter((id) => id !== factor.id)
    if (signIn.complete) {
      await finish()
      return
    }
    // The used factor is gone. With several methods left, return to the
    // chooser instead of guessing which one the next step should use.
    selectedFactorId.value = null
    await autoSelect()
  } catch (caught) {
    error.value = messageOf(caught)
  }
}

/**
 * Leaves for wherever this sign-in ends. A hand-off to another app is a
 * full-page navigation: that app owns its own router, and the session is
 * already in the cookie both origins share.
 */
async function leave() {
  const target = (await landingResolved).target
  if (landing.value?.external) {
    window.location.assign(target)
    return
  }
  await router.replace(target)
}

async function finish() {
  busy.value = true
  try {
    await signIn.exchange()
    await session.load()
    signIn.reset()
    notice.ok(t('signIn.noticeSignedIn'))
    await leave()
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
  mode.value = 'identify'
  started.value = false
  passkeyFactor.value = null
  selectedFactorId.value = null
  codeSentIds.value = []
  error.value = ''
  startPolling()
}

onMounted(() => {
  startPolling()
  void loadProviders()
  // A shared sign-in can be opened by someone who is already signed in (an app
  // links to it, or a previous round trip left the cookie). There is nothing to
  // ask them; resolve the target and hand them back.
  if (session.signedIn && landingRequested) void leave()
})
onBeforeUnmount(() => {
  if (pollTimer !== undefined) window.clearInterval(pollTimer)
})
</script>

<template>
  <div>
    <div class="mb-8">
      <h1 class="title mt-2 text-2xl text-base-content">
        {{ started ? t('signIn.continueTitle') : t('signIn.title') }}
      </h1>
      <p class="voice mt-2">
        {{ started ? t('signIn.continueIntro') : t('signIn.intro') }}
      </p>
    </div>

    <Transition name="step" mode="out-in">
    <QrLoginPanel v-if="mode === 'qr'" key="qr" @approved="qrApproved" @back="mode = 'identify'" />

    <form v-else-if="!started" key="identify" @submit.prevent="identify">
      <label for="account" class="field-label">{{ t('signIn.accountLabel') }}</label>
      <input
        id="account"
        v-model="account"
        class="input"
        autocomplete="username"
        autocapitalize="off"
        spellcheck="false"
        :placeholder="t('signIn.accountPlaceholder')"
        :disabled="busy"
      />
      <button type="submit" class="btn btn-primary mt-4 w-full" :disabled="busy || !account.trim()">
        {{ busy ? t('signIn.checking') : t('common.continue') }}
      </button>

      <div v-if="deviceWays.length || providers.length" class="mt-5 pt-1">
        <p class="field-label">{{ t('signIn.orContinueWith') }}</p>
        <div v-if="deviceWays.length" class="grid grid-cols-2 gap-2">
          <button
            v-if="passkeySupported"
            type="button"
            class="btn btn-outline"
            :disabled="busy"
            @click="passkeyDiscoverable"
          >
            <FingerprintRecognition16 aria-hidden="true" />
            {{ t('signIn.passkeyTile') }}
          </button>
          <button
            v-if="qrServed"
            type="button"
            class="btn btn-outline"
            :disabled="busy"
            @click="mode = 'qr'"
          >
            <QrCode16 aria-hidden="true" />
            {{ t('signIn.qrTile') }}
          </button>
        </div>
        <div
          v-if="providers.length"
          class="flex flex-wrap items-center gap-2"
          :class="{ 'mt-2': deviceWays.length }"
        >
          <a
            v-for="provider in providers"
            :key="provider"
            :href="landingPending ? undefined : providerUrl(provider)"
            class="btn btn-outline btn-square size-10 text-base-content"
            :class="{ 'pointer-events-none opacity-50': landingPending }"
            :aria-disabled="landingPending"
            :title="providerLabel(provider)"
            :aria-label="t('signIn.continueWith', { provider: providerLabel(provider) })"
          >
            <ProviderMark :provider="provider" />
          </a>
        </div>
      </div>
    </form>

    <div v-else key="challenge">
      <header class="flex items-center justify-between pb-3">
        <div class="min-w-0">
          <p class="truncate text-xs text-base-content">{{ signIn.account }}</p>
          <p class="text-2xs text-faint">
            {{
              t('signIn.stepsRemaining', {
                remain: signIn.challenge?.step_remain,
                total: signIn.challenge?.step_total,
              })
            }}
            <template v-if="signIn.challenge?.expired_at">
              · {{ t('signIn.timeLeft', { time: remaining(signIn.challenge.expired_at) }) }}
            </template>
          </p>
        </div>
        <button type="button" class="text-2xs text-dim hover:text-base-content" @click="restart">
          {{ t('signIn.changeAccount') }}
        </button>
      </header>

      <div class="pt-4">
        <button
          v-if="canUsePasskey"
          type="button"
          class="btn btn-outline mb-4 w-full"
          :disabled="busy"
          @click="passkeyBound"
        >
          <FingerprintRecognition16 aria-hidden="true" />
          {{ t('signIn.passkeyVerify') }}
        </button>

        <p v-if="!signIn.factors.length" class="py-3 text-xs text-dim">
          {{ t('signIn.waitingOther') }}
        </p>

        <div v-else-if="choiceFactors.length" class="relative">
          <Transition name="step" mode="out-in">
            <div v-if="!selectedFactor" key="choose">
              <p class="field-label">{{ t('signIn.chooseMethod') }}</p>
              <div class="space-y-1.5">
                <button
                  v-for="factor in choiceFactors"
                  :key="factor.id"
                  type="button"
                  class="flex w-full flex-col border border-line-soft px-3 py-2.5 text-left transition-colors hover:border-line hover:bg-base-300/60"
                  :disabled="busy"
                  @click="chooseFactor(factor)"
                >
                  <span class="text-xs text-base-content">
                    {{ factorTypeLabel(factor.type) }}
                  </span>
                  <span class="mt-0.5 text-2xs text-faint">
                    {{ factorTypeDescription(factor.type) }}
                  </span>
                </button>
              </div>
            </div>

            <div v-else-if="selectedFactor" key="chosen">
              <div v-if="choiceFactors.length > 1" class="mb-2 flex justify-end">
                <button
                  type="button"
                  class="text-2xs text-dim hover:text-base-content"
                  @click="selectedFactorId = null"
                >
                  {{ t('signIn.useAnother') }}
                </button>
              </div>
              <FactorRow
                :factor="selectedFactor"
                :busy="busy"
                :code-sent="codeSentIds.includes(selectedFactor.id)"
                :needs-code="needsCode(selectedFactor)"
                @send-code="sendCode(selectedFactor)"
                @submit="(secret) => submit(selectedFactor, secret)"
              />
            </div>
          </Transition>
        </div>
      </div>

      <p v-if="error || signIn.error" class="mt-3 text-xs text-error">
        {{ error || signIn.error }}
      </p>
    </div>
    </Transition>

    <div v-if="started && !selectedFactor" class="mt-4 flex items-center gap-2 text-2xs text-faint">
      <span class="dot animate-pulse bg-info" aria-hidden="true" />
      <span>{{ t('signIn.watching') }}</span>
    </div>

    <div class="mt-8 space-y-1 voice text-[12px] text-faint">
      <p>
        {{ t('signIn.noAccount') }}
        <RouterLink
          :to="{ name: 'create-account', query: landingQuery(route.query) }"
          class="link-quiet"
          >{{ t('signIn.createOne') }}</RouterLink
        >
      </p>
      <p>
        {{ t('signIn.lostPassword') }}
        <RouterLink
          :to="{ name: 'recover', query: landingQuery(route.query) }"
          class="link-quiet"
          >{{ t('signIn.resetByEmail') }}</RouterLink
        >
      </p>
    </div>
  </div>
</template>
