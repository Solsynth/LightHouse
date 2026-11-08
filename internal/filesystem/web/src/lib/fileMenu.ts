/**
 * The one file context menu the app shows at a time.
 *
 * A right-click belongs to the object under the pointer, but a selection is a
 * first-class thing too: when the pointer lands on a ticked row, the menu acts
 * on the whole selection. The state lives here rather than in a row component
 * so every surface (list, grid, waterfall, columns) opens the same menu and it
 * survives moving between rows.
 */

import { ref } from 'vue'

import type { CloudFile } from './api/types'

export interface FileMenuState {
  /** Objects the menu acts on; more than one only for a ticked selection. */
  files: CloudFile[]
  /** Viewport coordinates of the pointer that opened it. */
  x: number
  y: number
  /** A fresh key per opening, so the plate re-runs its entrance. */
  key: number
}

const state = ref<FileMenuState | null>(null)
let sequence = 0

/** Opens the menu for `file`, unless it is part of a larger ticked selection. */
export function openFileMenu(event: MouseEvent, file: CloudFile, selected: CloudFile[] = []) {
  event.preventDefault()
  event.stopPropagation()
  const files =
    selected.length > 1 && selected.some((entry) => entry.id === file.id) ? selected : [file]
  state.value = { files, x: event.clientX, y: event.clientY, key: ++sequence }
}

export function closeFileMenu() {
  state.value = null
}

export function useFileMenu() {
  return { state, closeFileMenu }
}
