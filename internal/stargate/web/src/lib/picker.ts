/**
 * Borrowing SnFS files for Stargate's own art slots.
 *
 * Avatar, background, and OAuth-app art are all Drive references: the profile
 * patch takes a file id and Stargate resolves it through the Drive client. The
 * picking itself is SnFS's job — its `/picker` page is embedded in a frame and
 * keeps the drive's session there, so this app never handles a file token or
 * proxies bytes.
 *
 * The embed SDK is loaded on first use rather than bundled: it is only needed
 * when someone opens the picker, and a Stargate deployment that never touches
 * profile art should not pay for it.
 */

import { SNFS_ORIGIN } from './media'
import type { PickerFile } from './picker-types'

export type { PickerFile }

/** The subset of the picker SDK this app calls. */
interface PickerSdk {
  openSnfsPicker: (
    config: { frameUrl: string; dialogTitle?: string; width?: number; height?: number },
    options: {
      view?: 'files' | 'folders' | 'any'
      multiple?: boolean
      mimeTypes?: string[]
      title?: string
      description?: string
      upload?: boolean
      theme?: 'auto' | 'dark' | 'light'
      locale?: 'en' | 'zh-CN'
    },
    handlers?: { onReady?: () => void; onSignedOut?: () => void; onError?: (m: string) => void },
  ) => Promise<PickerFile[]>
}

declare global {
  interface Window {
    openSnfsPicker?: PickerSdk['openSnfsPicker']
  }
}

/**
 * Where the picker page lives. `VITE_SNFS_URL` points a dev or self-hosted
 * console at its own drive; the file reads go to `DRIVE_API` instead, because
 * the fleet serves the page and the API from different hosts.
 */
export const PICKER_FRAME_URL = `${SNFS_ORIGIN}/picker`

let loading: Promise<PickerSdk> | null = null

/** Loads the picker script once; a failed load clears so a retry can work. */
function loadSdk(): Promise<PickerSdk> {
  if (window.openSnfsPicker) return Promise.resolve({ openSnfsPicker: window.openSnfsPicker })
  if (loading) return loading

  loading = new Promise<PickerSdk>((resolve, reject) => {
    const script = document.createElement('script')
    script.src = `${SNFS_ORIGIN}/picker/snfs-picker.js`
    script.async = true
    script.onload = () => {
      if (window.openSnfsPicker) resolve({ openSnfsPicker: window.openSnfsPicker })
      else reject(new Error('The SnFS picker loaded without its entry point.'))
    }
    script.onerror = () => reject(new Error('The SnFS picker could not be loaded.'))
    document.head.append(script)
  }).catch((error: unknown) => {
    loading = null
    throw error
  })

  return loading
}

export interface PickImageOptions {
  title: string
  description?: string
  width?: number
  height?: number
}

export interface PickFilesOptions {
  title: string
  description?: string
  mimeTypes?: string[]
  multiple?: boolean
  width?: number
  height?: number
}

/**
 * Opens the drive picker for one or more files. The picker keeps the drive
 * session in its own frame and returns Drive references, so callers only ever
 * need the ids; the ticket service resolves the rest server-side. `upload` is
 * on because the common case is attaching a file that is not in the drive yet.
 */
export async function pickFiles(options: PickFilesOptions): Promise<PickerFile[]> {
  const sdk = await loadSdk()
  return sdk.openSnfsPicker(
    {
      frameUrl: PICKER_FRAME_URL,
      dialogTitle: options.title,
      width: options.width ?? 760,
      height: options.height ?? 620,
    },
    {
      view: 'files',
      multiple: options.multiple ?? true,
      mimeTypes: options.mimeTypes,
      upload: true,
      title: options.title,
      description: options.description,
      theme: document.documentElement.dataset.theme === 'stargate-light' ? 'light' : 'dark',
      locale: document.documentElement.lang === 'zh-CN' ? 'zh-CN' : 'en',
    },
  )
}

/**
 * The image-only picker used by profile art. It shares the generic picker so
 * the frame, theme and locale rules stay in one place.
 */
export async function pickImage(options: PickImageOptions): Promise<PickerFile | null> {
  const files = await pickFiles({
    ...options,
    mimeTypes: ['image/*'],
    multiple: false,
  })
  return files[0] ?? null
}
