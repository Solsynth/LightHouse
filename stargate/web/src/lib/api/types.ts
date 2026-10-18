// Canonical wire types for the Stargate API (internal/httpserver). Field
// names follow the server's snake_case JSON exactly; anything omitted here is
// omitted on purpose (the API never emits it).

export interface ApiError {
  code: string
  message: string
  status: number
  detail?: string
  traceId?: string
  errors?: Record<string, string[]>
  meta?: unknown
}

export interface Account {
  id: string
  name: string
  nick: string
  language: string
  region: string
  activated_at?: string
  is_superuser: boolean
  automated_id?: string
  profile?: Profile
  contacts?: Contact[]
  badges?: unknown[]
  perk_level: number
  created_at?: string
  updated_at?: string
  deleted_at?: string
}

/** One compiled-in module, as reported by GET /api/modules. */
export interface ModuleInfo {
  id: string
  kind: 'feature' | 'integration'
}

/** The resolved module set plus the capabilities it exposes. */
export interface ModulesState {
  modules: ModuleInfo[]
  capabilities: string[]
}

/**
 * Public social-graph summary for an account. `mutual_friends` is present only
 * when the request carried a session and the viewer is not the account itself.
 */
export interface RelationshipSummary {
  followers: number
  following: number
  mutual_friends?: number
}

/**
 * One directed row of the social graph (`account_relationships`). A friendship
 * is two rows, one in each direction, written on accept. `status` is the wire
 * enum: -100 blocked, -50 muted, 0 pending, 100 friends, 200 close friend.
 */
export interface Relationship {
  account_id: string
  account?: Account
  related_id: string
  related?: Account
  status: number
  alias?: string
  expired_at?: string
  degrade_to_status?: number
  created_at?: string
  updated_at?: string
  deleted_at?: string
}

/**
 * A friend as `GET /api/friends/overview` reports it. Presence is a
 * last-seen heuristic (online within five minutes), and activities are always
 * empty until the presence service gains a batch read.
 */
export interface FriendOverview {
  account: Account
  status: {
    account_id: string
    is_online: boolean
    is_idle: boolean
    label: string
    created_at?: string
    updated_at?: string
  }
  activities: unknown[]
}

export interface Profile {
  id: string
  first_name?: string
  middle_name?: string
  last_name?: string
  bio?: string
  gender?: string
  pronouns?: string
  time_zone?: string
  location?: string
  links?: Link[]
  username_color?: UsernameColor
  birthday?: string
  last_seen_at?: string
  active_badge?: unknown
  experience: number
  level: number
  leveling_progress: number
  social_credits: number
  social_credits_level: number
  picture?: { id?: string; url?: string }
  background?: { id?: string; url?: string }
  account_id: string
  created_at?: string
  updated_at?: string
}

export interface Link {
  name: string
  url: string
}

export interface UsernameColor {
  type: string
  value?: string
  direction?: string
  colors?: string[]
}

export interface Contact {
  id: string
  type: number
  verified_at?: string
  is_primary: boolean
  is_public: boolean
  content: string
  account_id: string
  created_at?: string
  updated_at?: string
}

export interface AuthFactor {
  id: string
  type: number
  trustworthy: number
  enabled_at?: string
  expired_at?: string
  account_id: string
  created_at?: string
  updated_at?: string
  created_response?: Record<string, unknown>
}

export interface GeoPoint {
  latitude?: number
  longitude?: number
  country_code?: string
  country?: string
  city?: string
}

export interface AuthSession {
  id: string
  label?: string
  last_granted_at?: string
  expired_at?: string
  audiences: string[]
  scopes: string[]
  ip_address?: string
  user_agent?: string
  location?: GeoPoint
  type: number
  account_id: string
  created_at?: string
  updated_at?: string
  is_current?: boolean
  children_count?: number
  category?: string
  trusted?: boolean
  is_online: boolean
}

export interface Device {
  id: string
  platform: number
  device_name: string
  device_label?: string
  device_id: string
  account_id: string
  category?: string
  trusted?: boolean
  is_online: boolean
  sessions: AuthSession[]
}

export interface Connection {
  id: string
  provider: string
  provided_identifier: string
  meta?: Record<string, unknown>
  last_used_at?: string
  is_public: boolean
  account_id: string
  created_at?: string
  updated_at?: string
  registered_at?: string
}

export interface AuthorizedApp {
  id: string
  app_id: string
  type: number
  app_slug?: string
  app_name?: string
  app_description?: string
  scopes: string[]
  last_authorized_at?: string
  last_used_at?: string
}

/**
 * An OAuth app registration owned by the caller (Stargate's own registry,
 * not Develop). `status` follows the fleet's CustomAppStatus ordinal.
 */
export interface OAuthApp {
  id: string
  slug: string
  name: string
  description: string
  status: number
  project_id?: string
  account_id?: string
  oauth_config: OAuthAppConfig
  links: OAuthAppLinks
  /** Resolved Drive references; absent when the app has no art. */
  picture?: CloudFileRef
  background?: CloudFileRef
  created_at?: string
  updated_at?: string
}

export interface OAuthAppConfig {
  client_uri?: string
  redirect_uris: string[]
  post_logout_redirect_uris?: string[]
  allowed_scopes: string[]
  allowed_grant_types: string[]
  require_pkce: boolean
  allow_offline_access: boolean
  is_public_client: boolean
}

export interface OAuthAppLinks {
  home_page?: string
  privacy_policy?: string
  terms_of_service?: string
}

/**
 * One client secret or API key of an app. The plaintext `secret` is present
 * only in the create and rotate replies; every other read omits it.
 */
export interface OAuthAppSecret {
  id: string
  app_id: string
  secret?: string
  description: string
  expired_at?: string
  is_oidc: boolean
  created_at?: string
  updated_at?: string
}

export interface ApiKey {
  id: string
  label: string
  app_id?: string
  created_at?: string
  expired_at?: string
}

export interface ApiKeyCreated extends ApiKey {
  token: string
}

export interface AuthChallenge {
  id: string
  expired_at?: string
  step_remain: number
  step_total: number
  failed_attempts: number
  blacklist_factors: string[]
  audiences: string[]
  scopes: string[]
  ip_address?: string
  user_agent?: string
  device_id: string
  device_name?: string
  platform: number
  location?: GeoPoint
  account_id: string
  approved_at?: string
  declined_at?: string
  created_at?: string
  updated_at?: string
  purpose?: string
  sudo_until?: string
}

export interface TokenPair {
  token: string
  refresh_token: string
  expires_in: number
  refresh_expires_in: number
}

export interface AuthIdentity {
  id: string
  name: string
  nick: string
  language: string
  region: string
  is_superuser: boolean
  activated_at?: string
  session_id: string
  token_type: string
}

export interface ActionLog {
  id: string
  action: string
  meta?: Record<string, unknown>
  user_agent?: string
  ip_address?: string
  location?: GeoPoint
  account_id: string
  session_id?: string
  created_at?: string
}

export interface AdminAccountSummary {
  account: Account
  primary_email?: string
  contact_count: number
  auth_factor_count: number
  has_password: boolean
  active_session_count: number
  active_device_count: number
  active_punishment?: Punishment
}

export interface AdminAuthFactor {
  id: string
  type: number
  trustworthy: number
  has_secret: boolean
  config?: Record<string, unknown>
  enabled_at?: string
  expired_at?: string
  created_at?: string
  updated_at?: string
}

export interface AdminAccountDetail {
  account: Account
  contacts: Contact[]
  auth_factors: AdminAuthFactor[]
  active_session_count: number
  active_device_count: number
  active_punishment?: Punishment
  active_punishments: Punishment[]
  punishment_overview?: Punishment
  connection_count: number
  passkey_count: number
}

export interface Punishment {
  id: string
  reason: string
  expired_at?: string
  type: number
  blocked_permissions?: string[]
  account_id: string
  creator_id?: string
  created_at?: string
  account?: Account
  creator?: Account
}

export interface PermissionGroup {
  id: string
  key: string
  node_count: number
  member_count: number
  created_at?: string
  updated_at?: string
}

export interface PermissionNode {
  id: string
  type: number
  actor: string
  key: string
  value?: unknown
  expired_at?: string
  affected_at?: string
  group_id?: string
  created_at?: string
  updated_at?: string
}

export interface PermissionMember {
  group_id: string
  actor: string
  expired_at?: string
  affected_at?: string
  created_at?: string
  updated_at?: string
}

export interface PermissionGroupDetail {
  group: { id: string; key: string; created_at?: string; updated_at?: string }
  nodes: PermissionNode[]
  node_total: number
  members: PermissionMember[]
  member_total: number
}

export interface ActorPermissions {
  actor: string
  direct_permissions: PermissionNode[]
  effective_permissions: PermissionNode[]
  groups: Array<PermissionMember & { group_key: string }>
}

export interface CacheStats {
  keyspace_hits: number
  keyspace_misses: number
  total_commands_processed: number
  evicted_keys: number
  expired_keys: number
  connected_clients: number
  used_memory_bytes: number
  read_count: number
  hit_ratio: number
}

export interface CacheGroup {
  group: string
  count: number
  keys: string[]
}

export interface GeographyBucket {
  country_code: string
  country?: string
  city?: string
  latitude: number
  longitude: number
  user_count: number
}

export interface CaptchaConfig {
  provider: string
  apiKey: string
}

export interface PasskeyLoginStart {
  challenge: string
  rp_id: string
  allow_credentials: Array<{ type: string; id: string; transports: string[] }>
  timeout: number
  user_verification: string
  auth_challenge_id: string
}

/** Credential row returned by GET /api/factors/passkey. */
export interface Passkey {
  id: string
  label: string
  created_at?: string
  updated_at?: string
}

/** Server-issued options for a WebAuthn registration ceremony. */
export interface PasskeyRegistrationStart {
  challenge: string
  rp_id: string
  rp_name: string
  user_id: string
  user_name: string
  display_name: string
  pub_key_cred_params: Array<{ type: string; alg: number }>
  timeout: number
}

/**
 * A Drive file reference as the API serializes it. `url` addresses the file
 * directly; every other field is render metadata.
 */
export interface CloudFileRef {
  id: string
  name?: string
  url?: string
  mime_type?: string
  size?: number
  hash?: string
  blurhash?: string
  width?: number
  height?: number
}

/** Client metadata returned by GET /api/auth/open/authorize. */
export interface AuthorizeClientInfo {
  client_id: string
  client_name?: string
  picture?: CloudFileRef
  background?: CloudFileRef
  home_uri?: string
  policy_uri?: string
  terms_of_service_uri?: string
  response_types?: string
  scopes: string[]
  state?: string
  nonce?: string
  code_challenge?: string
  code_challenge_method?: string
}

/** The reply to an approve/deny decision. */
export interface AuthorizeDecision {
  redirect_uri?: string
}

/**
 * GET /api/auth/open/device/code/{userCode}. `status` is the device status the
 * provider already resolved to a word: pending, approved, declined, expired.
 */
export interface DeviceCodeStatus {
  user_code: string
  client_id: string
  client_name: string
  client_slug: string
  picture?: CloudFileRef
  background?: CloudFileRef
  scopes: string[]
  status: 'pending' | 'approved' | 'declined' | 'expired'
  expires_at?: string
  expires_in: number
  interval: number
  verification_uri: string
}

/** POST /api/auth/qr/generate reply: the payload a phone scans, plus the
 *  challenge the web page polls and eventually exchanges. */
export interface QrChallengeCreated {
  qr_challenge_id: string
  auth_challenge_id: string
  qr_data: string
  expires_at?: string
  expires_in_seconds: number
}

/** GET /api/auth/qr/{id} reply. `status` is the QrLoginStatus ordinal. */
export interface QrChallengeStatus {
  qr_challenge_id: string
  auth_challenge_id: string
  status: number
  expires_at?: string
  approved_at?: string
  approved_device_id?: string
  device_name?: string
  platform: number
}

/**
 * A magic spell as `GET /api/spells/{word}` reports it. The secret word is
 * never serialized (`json:"-"` on the server), so the page can render what it
 * is and whose it is without holding a credential it could leak.
 */
export interface MagicSpell {
  id: string
  /** MagicSpellType ordinal: 3 password reset, 4 contact verification. */
  type: number
  expires_at?: string
  affected_at?: string
  meta: Record<string, unknown>
  account_id?: string
  account?: Account
  created_at?: string
  updated_at?: string
}
