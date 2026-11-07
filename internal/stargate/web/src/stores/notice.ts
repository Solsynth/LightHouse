import { defineStore } from 'pinia'
import { ref } from 'vue'

interface Notice {
  id: number
  tone: 'ok' | 'error'
  message: string
}

let nextId = 1

/** Errors carry a deadline a reader has time to notice; confirmations leave sooner. */
const LIFETIME: Record<Notice['tone'], number> = { ok: 5000, error: 12000 }

/**
 * The console's one-line status channel. Every action result — success or
 * failure — is announced here, in the shell's floating status stack, rather
 * than in a note that scrolls away with the panel that produced it.
 */
export const useNoticeStore = defineStore('notice', () => {
  const items = ref<Notice[]>([])

  function push(tone: Notice['tone'], message: string) {
    const id = nextId++
    items.value = [...items.value.slice(-2), { id, tone, message }]
    window.setTimeout(() => dismiss(id), LIFETIME[tone])
  }

  function dismiss(id: number) {
    items.value = items.value.filter((item) => item.id !== id)
  }

  const ok = (message: string) => push('ok', message)
  const fail = (message: string) => push('error', message)

  return { items, ok, fail, push, dismiss }
})
