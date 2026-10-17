import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { onElevationRequired, request, StargateError } from '@/lib/api/client'
import type { Account, AuthChallenge, AuthIdentity } from '@/lib/api/types'

/** Shape the sudo challenge returns; only the fields the console reads. */
interface ElevationChallenge extends AuthChallenge {
  sudo_until?: string
}

export const useSessionStore = defineStore('session', () => {
  const identity = ref<AuthIdentity | null>(null)
  const account = ref<Account | null>(null)
  const loading = ref(true)
  const elevationUntil = ref<number | null>(null)
  const elevationOpen = ref(false)
  // Bumped on every grant so views remount and re-run the action that was
  // refused, instead of asking the person to click it again.
  let settleElevation: (() => void) | null = null

  // Any gated call that comes back with AUTH_SUDO_REQUIRED opens the sheet and
  // pauses that request. The client retries it once the grant (or dismissal)
  // settles, so callers do not have to remember to check.
  onElevationRequired(
    () =>
      new Promise<void>((resolve) => {
        settleElevation = resolve
        elevationOpen.value = true
      }),
  )

  const signedIn = computed(() => identity.value !== null)
  const elevated = computed(
    () => elevationUntil.value !== null && elevationUntil.value > Date.now(),
  )

  /**
   * Resolves the current session once at boot. A 401 is the normal signed-out
   * state, so it clears the store instead of surfacing an error.
   */
  async function load() {
    loading.value = true
    try {
      identity.value = await request<AuthIdentity>('/auth/me')
      account.value = await request<Account>('/accounts/me')
    } catch (error) {
      if (error instanceof StargateError && error.isUnauthenticated) {
        identity.value = null
        account.value = null
      } else {
        throw error
      }
    } finally {
      loading.value = false
    }
  }

  /** Re-reads the account after a profile or security change. */
  async function refreshAccount() {
    if (!identity.value) return
    account.value = await request<Account>('/accounts/me')
  }

  async function signOut() {
    try {
      await request<void>('/auth/logout', { method: 'POST' })
    } finally {
      identity.value = null
      account.value = null
      elevationUntil.value = null
    }
  }

  /** Records a completed elevation so gated controls unlock immediately. */
  function noteElevation(challenge: ElevationChallenge) {
    if (challenge.sudo_until) {
      elevationUntil.value = new Date(challenge.sudo_until).getTime()
    }
    elevationOpen.value = false
    settleElevation?.()
    settleElevation = null
  }

  /** Releases a paused request when the person abandons the sheet. */
  function dismissElevation() {
    elevationOpen.value = false
    settleElevation?.()
    settleElevation = null
  }

  return {
    identity,
    account,
    loading,
    elevationUntil,
    elevationOpen,
    signedIn,
    elevated,
    load,
    refreshAccount,
    signOut,
    noteElevation,
    dismissElevation,
  }
})
