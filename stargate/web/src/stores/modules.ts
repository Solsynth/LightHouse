import { computed, ref } from 'vue'
import { defineStore } from 'pinia'

import { modulesApi } from '@/lib/api'

/**
 * The enabled module set, read once from GET /api/modules.
 *
 * The server resolves its compiled-in modules at boot, so a deployment may
 * serve no relationships, no profiles, no admin surface at all. The console
 * asks the same question instead of guessing: nav entries, routes and profile
 * panels are gated on the modules that are actually mounted.
 */
export const useModulesStore = defineStore('modules', () => {
  const ids = ref<string[]>([])
  const capabilities = ref<string[]>([])
  // loaded flips only on a successful read. An unreachable /api/modules (an
  // older server, a proxy that drops the route) must not blank the console,
  // so an unloaded store gates nothing.
  const loaded = ref(false)

  const enabled = computed(() => new Set(ids.value))

  /**
   * True when every listed module is enabled. An empty requirement always
   * passes, and an unloaded store fails open so a transient read failure never
   * hides a surface the deployment actually serves.
   */
  function hasAll(required: string[]): boolean {
    if (!loaded.value) return true
    return required.every((id) => enabled.value.has(id))
  }

  /** Reads the module set once per page load; failures leave it unloaded. */
  async function load() {
    if (loaded.value) return
    try {
      const state = await modulesApi.state()
      ids.value = state.modules.map((module) => module.id)
      capabilities.value = state.capabilities
      loaded.value = true
    } catch {
      // Leave loaded false: gates stay open.
    }
  }

  return { ids, capabilities, loaded, enabled, hasAll, load }
})
