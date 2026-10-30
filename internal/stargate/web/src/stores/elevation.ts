import { defineStore } from 'pinia'
import { ref } from 'vue'

import { request } from '@/lib/api/client'
import type { AuthChallenge } from '@/lib/api/types'

export interface ElevationState {
  challenge: AuthChallenge
  error: string
  busy: boolean
}

/**
 * Elevation ("sudo") is a two-step flow: create a challenge, then satisfy its
 * factors. The sheet owns the state because it must survive route changes
 * while the person is being re-verified.
 */
export const useElevationStore = defineStore('elevation', () => {
  const state = ref<ElevationState | null>(null)

  async function begin() {
    const challenge = await request<AuthChallenge>('/auth/sudo', { method: 'POST' })
    state.value = { challenge, error: '', busy: false }
    return challenge
  }

  function reset() {
    state.value = null
  }

  return { state, begin, reset }
})
