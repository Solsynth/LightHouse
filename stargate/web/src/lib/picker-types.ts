/**
 * The wire shape of a picked file, as `snfs-picker.js` sends it.
 *
 * Stargate only reads `id` (it stores a Drive reference and resolves the rest
 * server-side), so this is the subset plus the fields a caller may log or
 * preview. It is redeclared rather than imported because the picker ships as a
 * plain script from another origin — there is no module to import at build
 * time, and a type-only import from `DysonFS/web` would couple the two
 * repositories.
 */
export interface PickerFile {
  id: string
  name: string
  is_folder: boolean
  mime_type: string
  size: number
  extension: string
  kind: string
  parent_id: string | null
  created_at: string
  updated_at: string
  /** Same-origin with the drive; carries the drive's session cookie. */
  url: string
  thumbnail_url: string | null
  file: Record<string, unknown>
}
