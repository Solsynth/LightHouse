import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { request } from '@/lib/api/client'
import type { AuthChallenge, AuthFactor, TokenPair } from '@/lib/api/types'
import { deviceIdentity } from '@/lib/device'
import { translate } from '@/lib/i18n'

/** Web platform value the server expects (model.ClientPlatformWeb). */
const PLATFORM_WEB = 1

/**
 * Anonymous sign-in: a challenge names the account and its remaining steps,
 * the server offers the usable factors, and a completed challenge is
 * exchanged for the session cookies.
 */
export const useSignInStore = defineStore('signIn', () => {
  const challenge = ref<AuthChallenge | null>(null)
  const factors = ref<AuthFactor[]>([])
  const usedFactorIds = ref<string[]>([])
  const busy = ref(false)
  const error = ref('')

  const account = ref('')
  const password = ref('')

  const complete = computed(() => challenge.value !== null && challenge.value.step_remain === 0)
  const remainingSteps = computed(() => challenge.value?.step_remain ?? 0)

  async function start(accountName: string) {
    const device = deviceIdentity()
    challenge.value = await request<AuthChallenge>('/auth/challenge', {
      method: 'POST',
      body: {
        account: accountName,
        device_id: device.id,
        device_name: device.name,
        platform: PLATFORM_WEB,
      },
    })
    usedFactorIds.value = []
    account.value = accountName
    await loadFactors()
  }

  /** Loads the factors this challenge accepts, skipping the ones already used. */
  async function loadFactors() {
    if (!challenge.value) return
    const list = await request<AuthFactor[]>(
      `/auth/challenge/${challenge.value.id}/factors`,
    )
    const blacklist = new Set(challenge.value.blacklist_factors)
    factors.value = list.filter(
      (factor) => !blacklist.has(factor.id) && !usedFactorIds.value.includes(factor.id),
    )
  }

  /** Asks the server to send a one-time code for a code factor. */
  async function requestCode(factorId: string) {
    if (!challenge.value) return
    await request<void>(`/auth/challenge/${challenge.value.id}/factors/${factorId}`, {
      method: 'POST',
    })
  }

  /** Submits one factor's secret and refreshes the remaining-step count. */
  async function submitFactor(factorId: string, secret: string) {
    if (!challenge.value) return
    busy.value = true
    error.value = ''
    try {
      challenge.value = await request<AuthChallenge>(
        `/auth/challenge/${challenge.value.id}`,
        { method: 'PATCH', body: { factor_id: factorId, password: secret } },
      )
      usedFactorIds.value = [...usedFactorIds.value, factorId]
      password.value = ''
      if (!complete.value) await loadFactors()
    } catch (caught) {
      error.value = caught instanceof Error ? caught.message : translate('errors.credentialRejected')
      throw caught
    } finally {
      busy.value = false
    }
  }

  /** Redeems the finished challenge; the server sets the session cookies. */
  async function exchange() {
    if (!challenge.value) return null
    const pair = await request<TokenPair>('/auth/token', {
      method: 'POST',
      body: { grant_type: 'authorization_code', code: challenge.value.id },
    })
    return pair
  }

  /** Polls the challenge so a cross-device approval lands without a reload. */
  async function poll() {
    if (!challenge.value) return
    challenge.value = await request<AuthChallenge>(`/auth/challenge/${challenge.value.id}`)
    if (challenge.value.step_remain > 0) await loadFactors()
  }

  function reset() {
    challenge.value = null
    factors.value = []
    usedFactorIds.value = []
    account.value = ''
    password.value = ''
    error.value = ''
  }

  return {
    challenge,
    factors,
    usedFactorIds,
    busy,
    error,
    account,
    password,
    complete,
    remainingSteps,
    start,
    loadFactors,
    requestCode,
    submitFactor,
    exchange,
    poll,
    reset,
  }
})
