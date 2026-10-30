/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Origin of the SnFS drive that resolves file ids (default api.solian.app). */
  readonly VITE_SNFS_API_URL?: string
  /** Origin of the drive's picker page (default fs.solian.app). */
  readonly VITE_SNFS_URL?: string
}
