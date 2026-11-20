/** Wire types for the DysonFS master API. */

export interface FileObject {
  id: string
  size: number
  mime_type: string
  hash: string
  storage_key?: string | null
  meta?: Record<string, unknown> | null
  has_compression: boolean
  has_thumbnail: boolean
  created_at?: string
  updated_at?: string
}

export interface PermissionStatus {
  readable: boolean
  writable: boolean
  deletable: boolean
  shareable: boolean
  manageable: boolean
  visibility: 'public' | 'private' | string
  inherit_acl: boolean
  effective_mask: number
  inherited_from?: string | null
}

/** Permission bits mirror the server's PermissionMask. */
export const PERM_READ = 1
export const PERM_WRITE = 1 << 1
export const PERM_DELETE = 1 << 2
export const PERM_SHARE = 1 << 3
export const PERM_MANAGE = 1 << 4

export type ACLSubjectType =
  | 'account'
  | 'workspace'
  | 'workspace_role'
  | 'workspace_group'
  | 'authenticated'
  | 'public'
  | 'link'
  | 'scope'

export type ACLEffect = 'allow' | 'deny'

export interface FileACLEntry {
  id?: string
  file_id: string
  subject_type: ACLSubjectType
  subject_id: string
  effect: ACLEffect
  permissions: number
  inheritable: boolean
  expires_at?: string | null
  created_at?: string
  updated_at?: string
}

export interface InheritedFileACLEntry extends FileACLEntry {
  source_file_id: string
  source_file_name: string
}

export interface FileACLDetail {
  file_id: string
  inherit_acl: boolean
  direct: FileACLEntry[]
  inherited: InheritedFileACLEntry[]
  effective: PermissionStatus
}

export interface ACLEntryInput {
  subject_type: ACLSubjectType
  subject_id: string
  effect: ACLEffect
  role?: 'viewer' | 'editor' | 'contributor' | 'manager'
  permissions?: number
  inheritable?: boolean
  expires_at?: string | null
}

export interface CloudFile {
  id: string
  name: string
  description?: string | null
  account_id: string
  workspace_id?: string | null
  pool_id?: string | null
  object_id?: string | null
  parent_id?: string | null
  indexed: boolean
  is_folder: boolean
  is_marked_recycle: boolean
  expired_at?: string | null
  storage_url?: string | null
  storage_key?: string | null
  file_meta?: Record<string, unknown> | null
  user_meta?: Record<string, unknown> | null
  usage?: string | null
  application_type?: string | null
  status: number
  created_at: string
  updated_at: string
  object?: FileObject | null
  children?: CloudFile[] | null
  children_count?: number
  permission_status?: PermissionStatus
}

export interface BreadcrumbItem {
  id: string
  name: string
  parent_id?: string | null
  is_folder: boolean
}

export interface FilePool {
  id: string
  name: string
  description: string
  account_id: string
  storage_config: {
    enable_signed?: boolean
    enable_ssl?: boolean
    endpoint?: string
    bucket?: string
  }
  billing_config: { cost_multiplier?: number | null }
  policy_config: {
    public_usable?: boolean
    max_file_size?: number | null
    accept_types?: string[] | null
  }
  is_hidden: boolean
  created_at: string
  updated_at: string
}

export interface QuotaSummary {
  based_quota: number
  leveling_quota: number
  perk_quota: number
  extra_quota: number
  total_quota: number
}

export interface UsageSummary {
  used_quota: number
  total_quota: number
  total_file_count: number
  total_usage_bytes: number
}

export interface PoolUsage {
  pool_id: string
  total_quota: number
}

export interface AccountIdentity {
  id: string
  name: string
  nick: string
  is_superuser: boolean
  session_id?: string
}

export type FileOrder = 'date' | 'name' | 'size'
export type KindFilter = 'all' | 'folders' | 'files'
export type ViewMode = 'list' | 'grid' | 'column' | 'waterfall'

export interface ListQuery {
  offset?: number
  take?: number
  query?: string
  order?: FileOrder
  orderDesc?: boolean
  pool_id?: string
  is_folder?: boolean
  recycled?: boolean
  indexed?: boolean
}
