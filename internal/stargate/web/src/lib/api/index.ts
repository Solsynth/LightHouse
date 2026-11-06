import { request, requestBlob, requestForm, requestPage, sendString } from './client'
import type { Page } from './client'
import type {
  Account,
  ActionLog,
  ActorPermissions,
  AdminAccountDetail,
  AdminAccountSummary,
  ApiKey,
  ApiKeyCreated,
  AuthChallenge,
  AuthFactor,
  AuthSession,
  AuthorizeClientInfo,
  AuthorizeDecision,
  AuthorizedApp,
  CacheGroup,
  CacheStats,
  CaptchaConfig,
  Connection,
  Contact,
  Device,
  DeviceCodeStatus,
  GeographyBucket,
  HandoffIssue,
  MagicSpell,
  Passkey,
  PasskeyLoginStart,
  PasskeyRegistrationStart,
  PermissionGroup,
  PermissionGroupDetail,
  PermissionMember,
  PermissionNode,
  Profile,
  QrChallengeCreated,
  SignInConfig,
  QrChallengeStatus,
  Punishment,
  FriendOverview,
  ModulesState,
  Relationship,
  RelationshipSummary,
  AdminBadge,
  AdminBoardItem,
  AdminAuthFactor,
  AdminDevice,
  AdminRealm,
  AdminRealmDetail,
  NameHistoryEntry,
  OnCallAdmin,
  ProgressionDefinition,
  ProgressionUpsert,
  RealmMember,
  SessionChallenge,
  VerificationMark,
  WalletOrder,
  WalletProductSummary,
  WellbeingActivity,
  WellbeingStats,
} from './types'

export type { Page }

// ── Account ──

export const accountApi = {
  me: () => request<Account>('/accounts/me'),
  updateBasicInfo: (body: { nick?: string; language?: string; region?: string }) =>
    request<Account>('/accounts/me', { method: 'PATCH', body }),
  /**
   * The profile patch. `picture_id`/`background_id` carry a Drive file id: an
   * id stores it, and the empty string clears the art. They are write-only —
   * reads come back as resolved `picture`/`background` references.
   */
  updateProfile: (
    body: Partial<Record<keyof Profile, unknown>> & {
      picture_id?: string
      background_id?: string
    },
  ) => request<Profile>('/accounts/me/profile', { method: 'PATCH', body }),
  requestDeletion: () => request<void>('/accounts/me', { method: 'DELETE' }),
  search: (query: string, take = 20) =>
    request<Account[]>('/accounts/search', { query: { query, take } }),
  byName: (name: string) => request<Account>(`/accounts/${encodeURIComponent(name)}`),
  /** Public social-graph summary; mutual_friends needs a session on the call. */
  relationshipSummary: (name: string) =>
    request<RelationshipSummary>(`/accounts/${encodeURIComponent(name)}/relationships`),
}

// ── Relationships ──

/**
 * The social graph: friends, pending requests, blocks, mutes, close friends.
 * Every write answers with the row it changed, so callers reload rather than
 * patch their copy.
 */
export const relationshipApi = {
  /**
   * Every non-pending outgoing row, newest first. The server also reports the
   * full count in X-Total, which can exceed the rows returned.
   */
  list: (query: { take?: number; offset?: number } = {}) =>
    requestPage<Relationship>('/relationships', query),
  /** Pending rows in both directions: incoming requests and the ones sent. */
  requests: () => request<Relationship[]>('/relationships/requests'),
  closeFriends: () => request<Account[]>('/relationships/close-friends'),
  /** Friends with a last-seen presence label; activities stay empty. */
  overview: () => request<FriendOverview[]>('/friends/overview'),
  sendRequest: (accountId: string) =>
    request<Relationship>(`/relationships/${accountId}/friends`, { method: 'POST' }),
  cancelRequest: (accountId: string) =>
    request<void>(`/relationships/${accountId}/friends`, { method: 'DELETE' }),
  acceptRequest: (accountId: string) =>
    request<Relationship>(`/relationships/${accountId}/friends/accept`, { method: 'POST' }),
  declineRequest: (accountId: string) =>
    request<Relationship>(`/relationships/${accountId}/friends/decline`, { method: 'POST' }),
  /** Soft-deletes the outgoing row; the other side keeps its own. */
  removeFriend: (accountId: string) =>
    request<Relationship>(`/relationships/${accountId}`, { method: 'DELETE' }),
  block: (accountId: string) =>
    request<Relationship>(`/relationships/${accountId}/block`, { method: 'POST' }),
  unblock: (accountId: string) =>
    request<Relationship>(`/relationships/${accountId}/block`, { method: 'DELETE' }),
  mute: (accountId: string) =>
    request<Relationship>(`/relationships/${accountId}/mute`, { method: 'POST' }),
  unmute: (accountId: string) =>
    request<Relationship>(`/relationships/${accountId}/mute`, { method: 'DELETE' }),
  addCloseFriend: (accountId: string) =>
    request<Relationship>(`/relationships/${accountId}/close-friend`, { method: 'POST' }),
  removeCloseFriend: (accountId: string) =>
    request<Relationship>(`/relationships/${accountId}/close-friend`, { method: 'DELETE' }),
  /** An empty alias clears it; only the owner ever sees it. */
  updateAlias: (accountId: string, alias: string | null) =>
    request<Relationship>(`/relationships/${accountId}/alias`, {
      method: 'PATCH',
      body: { alias },
    }),
}

// ── Modules ──

/**
 * The resolved module set and capabilities. The console uses it to hide the
 * surfaces a deployment does not serve, so a disabled module never renders a
 * nav entry or a route that would 404.
 */
export const modulesApi = {
  state: () => request<ModulesState>('/modules'),
}

// ── Auth ──

export const authApi = {
  captchaConfig: () => request<CaptchaConfig>('/auth/captcha'),
  /** The shared login page's hand-off policy (cookie hosts + https rule). */
  signInConfig: () => request<SignInConfig>('/auth/sign-in/config'),
  providers: () => request<string[]>('/auth/providers'),
  /**
   * Mints a single-use code handing this session to the app at `redirect_uri`.
   * The console only names where the browser should land; the issuer resolves
   * which registered app that is.
   */
  handoffSession: (body: { redirect_uri: string }) =>
    request<HandoffIssue>('/auth/handoff/session', { method: 'POST', body }),
  handoffToken: (body: { target_uri?: string; redirect_uri?: string }) =>
    request<{ token: string; expires_in: number }>('/auth/handoff/token', { method: 'POST', body }),
  authorizeInfo: (query: Record<string, string>) =>
    request<AuthorizeClientInfo>('/auth/open/authorize', { query }),
  authorizeDecision: (form: URLSearchParams) =>
    requestForm<AuthorizeDecision>('/auth/open/authorize', form),
  /**
   * The device flow's two anonymous calls: the status the approval page polls
   * by user code, and the approve/decline it submits. The user code is the
   * secret here, so both stay unauthenticated; approving is what binds the
   * code to this session.
   */
  deviceCodeStatus: (userCode: string) =>
    request<DeviceCodeStatus>(`/auth/open/device/code/${encodeURIComponent(userCode)}`),
  deviceCodeApprove: (userCode: string) =>
    request<void>(`/auth/open/device/code/${encodeURIComponent(userCode)}/approve`, {
      method: 'POST',
    }),
  deviceCodeDecline: (userCode: string) =>
    request<void>(`/auth/open/device/code/${encodeURIComponent(userCode)}/decline`, {
      method: 'POST',
    }),
  logout: () => request<void>('/auth/logout', { method: 'POST' }),
  pendingChallenges: () => request<AuthChallenge[]>('/auth/challenge/pending'),
  approveChallenge: (id: string) =>
    request<void>(`/auth/challenge/${id}/approve`, { method: 'POST' }),
  declineChallenge: (id: string) =>
    request<void>(`/auth/challenge/${id}/decline`, { method: 'POST' }),
}

// ── Sign-in ──

export const signInApi = {
  startChallenge: (body: {
    account: string
    device_id: string
    device_name: string
    platform: number
  }) => request<AuthChallenge>('/auth/challenge', { method: 'POST', body }),
  challenge: (id: string) => request<AuthChallenge>(`/auth/challenge/${id}`),
  factors: (id: string) => request<AuthFactor[]>(`/auth/challenge/${id}/factors`),
  requestCode: (id: string, factorId: string) =>
    request<void>(`/auth/challenge/${id}/factors/${factorId}`, { method: 'POST' }),
  submitFactor: (id: string, factorId: string, secret: string) =>
    request<AuthChallenge>(`/auth/challenge/${id}`, {
      method: 'PATCH',
      body: { factor_id: factorId, password: secret },
    }),
  exchange: (code: string) =>
    request<{ token: string; refresh_token: string; expires_in: number }>('/auth/token', {
      method: 'POST',
      body: { grant_type: 'authorization_code', code },
    }),
  passkeyDiscoverableStart: (body: { device_id: string; device_name: string; platform: number }) =>
    request<PasskeyLoginStart>('/auth/passkey/start', { method: 'POST', body }),
  passkeyDiscoverableComplete: (
    challengeId: string,
    body: {
      credential_id: string
      client_data_json: string
      authenticator_data: string
      signature: string
      user_handle?: string
    },
  ) =>
    request<AuthChallenge>(`/auth/passkey/${challengeId}/complete`, {
      method: 'POST',
      body,
    }),
  passkeyStart: (challengeId: string) =>
    request<Omit<PasskeyLoginStart, 'auth_challenge_id'>>(
      `/auth/challenge/${challengeId}/passkey/start`,
      { method: 'POST' },
    ),
  passkeyComplete: (
    challengeId: string,
    body: {
      credential_id: string
      client_data_json: string
      authenticator_data: string
      signature: string
      user_handle?: string
    },
  ) =>
    request<AuthChallenge>(`/auth/challenge/${challengeId}/passkey/complete`, {
      method: 'POST',
      body,
    }),
  register: (body: {
    name: string
    nick: string
    email: string
    password: string
    language: string
    captcha_token?: string
  }) => request<Account>('/accounts', { method: 'POST', body }),
  validateRegistration: (body: { name?: string; email?: string }) =>
    request<void>('/accounts/validate', { method: 'POST', body }),
  requestPasswordReset: (body: { account: string; captcha_token: string }) =>
    request<void>('/accounts/recovery/password', { method: 'POST', body }),
  /**
   * Starts a QR login for this browser. The reply's `qr_data` is the payload a
   * signed-in device scans; the web page only polls `qrStatus` afterwards.
   */
  qrGenerate: (body: { device_id: string; device_name?: string; platform: number }) =>
    request<QrChallengeCreated>('/auth/qr/generate', { method: 'POST', body }),
  /** The status of a QR challenge: 0 pending, 1 scanned, 2 approved, 3 declined. */
  qrStatus: (id: string) => request<QrChallengeStatus>(`/auth/qr/${id}`),
  /**
   * Reads a magic spell by the secret word in its emailed link. An expired
   * spell answers 410 (PASSPORT_SPELL_EXPIRED), a wrong or used one 404.
   */
  getSpell: (word: string, signal?: AbortSignal) =>
    request<MagicSpell>(`/spells/${encodeURIComponent(word)}`, { signal }),
  /**
   * Redeems a spell. A password-reset spell consumes the word and installs the
   * new password; a contact-verification spell marks the contact verified.
   */
  applySpell: (word: string, newPassword?: string) =>
    request<void>(`/spells/${encodeURIComponent(word)}/apply`, {
      method: 'POST',
      body: newPassword === undefined ? undefined : { new_password: newPassword },
    }),
  /** Re-sends the email for a spell that never arrived. */
  resendSpell: (id: string) =>
    request<void>(`/spells/${encodeURIComponent(id)}/resend`, { method: 'POST' }),
}

// ── Security ──

export const securityApi = {
  factors: () => request<AuthFactor[]>('/factors'),
  createFactor: (type: number, secret?: string) =>
    request<AuthFactor>('/factors', { method: 'POST', body: { type, secret } }),
  enableFactor: (id: string, code?: string) =>
    request<AuthFactor>(`/factors/${id}/enable`, { method: 'POST', body: code ?? undefined }),
  disableFactor: (id: string) => request<AuthFactor>(`/factors/${id}/disable`, { method: 'POST' }),
  deleteFactor: (id: string) => request<void>(`/factors/${id}`, { method: 'DELETE' }),
  /**
   * Replaces the current password. The current secret is the proof; no emailed
   * magic spell and no elevation round trip are involved.
   */
  changePassword: (currentPassword: string, newPassword: string) =>
    request<void>('/factors/password', {
      method: 'POST',
      body: { current_password: currentPassword, new_password: newPassword },
    }),
  passkeys: () => request<Passkey[]>('/factors/passkey'),
  startPasskeyRegistration: (body: { device_id: string; device_name?: string }) =>
    request<PasskeyRegistrationStart>('/factors/passkey/start', { method: 'POST', body }),
  completePasskeyRegistration: (body: {
    device_id: string
    client_data_json: string
    attestation_object: string
    label: string
  }) => request<Passkey>('/factors/passkey/complete', { method: 'POST', body }),
  updatePasskey: (id: string, label: string) =>
    request<unknown>(`/factors/passkey/${id}`, { method: 'PATCH', body: { label } }),
  deletePasskey: (id: string) => request<void>(`/factors/passkey/${id}`, { method: 'DELETE' }),
  contacts: () => request<Contact[]>('/contacts'),
  createContact: (type: number, content: string) =>
    request<Contact>('/contacts', { method: 'POST', body: { type, content } }),
  verifyContact: (id: string) => request<Contact>(`/contacts/${id}/verify`, { method: 'POST' }),
  setPrimaryContact: (id: string) =>
    request<Contact>(`/contacts/${id}/primary`, { method: 'POST' }),
  setContactPublic: (id: string, isPublic: boolean) =>
    request<Contact>(`/contacts/${id}/public`, { method: isPublic ? 'POST' : 'DELETE' }),
  deleteContact: (id: string) => request<void>(`/contacts/${id}`, { method: 'DELETE' }),
  preferences: () => request<{ mode: string }>('/security/preferences'),
  updatePreferences: (mode: string) =>
    request<{ mode: string }>('/security/preferences', { method: 'PATCH', body: { mode } }),
}

// ── Sessions and devices ──

export const sessionApi = {
  list: (query: { take?: number; offset?: number; type?: number } = {}) =>
    requestPage<AuthSession>('/sessions', query),
  children: (id: string) => requestPage<AuthSession>(`/sessions/${id}/children`, {}),
  revoke: (id: string) => request<void>(`/sessions/${id}`, { method: 'DELETE' }),
  revokeCurrent: () => request<void>('/sessions/current', { method: 'DELETE' }),
  devices: (query: { take?: number; offset?: number } = {}) =>
    requestPage<Device>('/devices', query),
  // Both routes take the device key (`device_id`), not the row's client id.
  deleteDevice: (deviceId: string) =>
    request<void>(`/devices/${encodeURIComponent(deviceId)}`, { method: 'DELETE' }),
  renameDevice: (deviceId: string, label: string) =>
    sendString(`/devices/${encodeURIComponent(deviceId)}/label`, label),
  renameCurrentDevice: (label: string) => sendString('/devices/current/label', label),
}

// ── Apps and keys ──

export const appApi = {
  authorized: () => requestPage<AuthorizedApp>('/authorized-apps', {}),
  deauthorize: (id: string, type: number) =>
    request<void>(`/authorized-apps/${id}`, { method: 'DELETE', query: { type } }),
  updateScopes: (id: string, scopes: string[]) =>
    request<AuthorizedApp>(`/authorized-apps/${id}/scopes`, { method: 'POST', body: { scopes } }),
  keys: () => request<ApiKey[]>('/api-keys'),
  createKey: (label: string, expiredAt?: string) =>
    request<ApiKeyCreated>('/api-keys', { method: 'POST', body: { label, expired_at: expiredAt } }),
  revokeKey: (id: string) => request<void>(`/api-keys/${id}`, { method: 'DELETE' }),
  rotateKey: (id: string) =>
    request<{ token: string }>(`/api-keys/${id}/rotate`, { method: 'POST' }),
  connections: () => request<Connection[]>('/connections'),
  removeConnection: (id: string) => request<void>(`/connections/${id}`, { method: 'DELETE' }),
  setConnectionVisibility: (id: string, isPublic: boolean) =>
    request<Connection>(`/connections/${id}/visibility`, {
      method: 'POST',
      body: { is_public: isPublic },
    }),
}

// ── OAuth apps ──

// ── Activity ──

export const activityApi = {
  list: (query: { action?: string; take?: number; offset?: number } = {}) =>
    requestPage<ActionLog>('/actions', query),
}

// ── Admin ──

export const adminApi = {
  accounts: (
    query: {
      query?: string
      take?: number
      offset?: number
      activated?: boolean
      hasPunishment?: boolean
    } = {},
  ) => requestPage<AdminAccountSummary>('/admin/accounts', query),
  account: (name: string) =>
    request<AdminAccountDetail>(`/admin/accounts/${encodeURIComponent(name)}`),
  activate: (name: string) =>
    request<Account>(`/admin/accounts/${encodeURIComponent(name)}/activate`, { method: 'POST' }),
  revokeSessions: (name: string) =>
    request<unknown>(`/admin/accounts/${encodeURIComponent(name)}/sessions/revoke`, {
      method: 'POST',
    }),
  suspend: (
    name: string,
    body: { reason: string; type: number; expired_at?: string; revoke_sessions?: boolean },
  ) =>
    request<Punishment>(`/admin/accounts/${encodeURIComponent(name)}/suspend`, {
      method: 'POST',
      body,
    }),
  deleteAccount: (name: string) =>
    request<unknown>(`/admin/accounts/${encodeURIComponent(name)}`, { method: 'DELETE' }),
  deleteDevice: (name: string, deviceId: string) =>
    request<void>(`/admin/accounts/${encodeURIComponent(name)}/devices/${deviceId}`, {
      method: 'DELETE',
    }),
  renameDevice: (name: string, deviceId: string, label: string) =>
    sendString(`/admin/accounts/${encodeURIComponent(name)}/devices/${deviceId}/label`, label),
  groups: (query: { query?: string; take?: number; offset?: number } = {}) =>
    requestPage<PermissionGroup>('/admin/permissions/groups', query),
  group: (id: string) => request<PermissionGroupDetail>(`/admin/permissions/groups/${id}`),
  createGroup: (key: string) =>
    request<PermissionGroup>('/admin/permissions/groups', { method: 'POST', body: { key } }),
  deleteGroup: (id: string) =>
    request<void>(`/admin/permissions/groups/${id}`, { method: 'DELETE' }),
  grantPermission: (groupId: string, key: string, expiredAt?: string) =>
    request<PermissionNode>(
      `/admin/permissions/groups/${groupId}/permissions/${encodeURIComponent(key)}`,
      { method: 'PUT', body: { value: true, expired_at: expiredAt ?? null } },
    ),
  revokePermission: (groupId: string, key: string) =>
    request<void>(`/admin/permissions/groups/${groupId}/permissions/${encodeURIComponent(key)}`, {
      method: 'DELETE',
    }),
  addMember: (groupId: string, actor: string) =>
    request<PermissionMember>(`/admin/permissions/groups/${groupId}/members/${actor}`, {
      method: 'PUT',
      body: {},
    }),
  removeMember: (groupId: string, actor: string) =>
    request<void>(`/admin/permissions/groups/${groupId}/members/${actor}`, { method: 'DELETE' }),
  actor: (actor: string) =>
    request<ActorPermissions>(`/admin/permissions/actors/${encodeURIComponent(actor)}`),
  cacheStats: () => request<CacheStats>('/admin/cache/stats'),
  cacheGroup: (group: string) =>
    request<CacheGroup>(`/admin/cache/groups/${encodeURIComponent(group)}`),
  clearCacheKey: (key: string) =>
    request<{ scope: string; removed_count: number }>('/admin/cache/keys/clear', {
      method: 'POST',
      body: { key },
    }),
  clearCacheGroup: (group: string) =>
    request<{ scope: string; removed_count: number }>('/admin/cache/groups/clear', {
      method: 'POST',
      body: { group },
    }),
  clearCache: () =>
    request<{ scope: string; removed_count: number }>('/admin/cache/clear', { method: 'POST' }),
  geography: (precision: 'country' | 'city', since?: string) =>
    request<{
      calculated_at: string
      since: string
      precision: 'country' | 'city'
      accounts_with_location: number
      buckets: GeographyBucket[]
    }>('/admin/stats/users/geography', { query: { precision, since } }),
  stats: () => request<WellbeingStats>('/admin/stats'),
  activity: () => request<WellbeingActivity>('/admin/accounts/metrics/activity'),

  // ── Account subresources ──

  accountSessions: (
    name: string,
    query: { take?: number; offset?: number; activeOnly?: boolean; includeChildren?: boolean } = {},
  ) =>
    requestPage<AuthSession>(`/admin/accounts/${encodeURIComponent(name)}/sessions`, query),
  accountSessionChildren: (
    name: string,
    sessionId: string,
    query: { take?: number; offset?: number } = {},
  ) =>
    requestPage<AuthSession>(
      `/admin/accounts/${encodeURIComponent(name)}/sessions/${sessionId}/children`,
      query,
    ),
  accountSessionChallenge: (name: string, sessionId: string) =>
    request<SessionChallenge>(
      `/admin/accounts/${encodeURIComponent(name)}/sessions/${sessionId}/challenge`,
    ),
  revokeAccountSession: (name: string, sessionId: string) =>
    request<void>(`/admin/accounts/${encodeURIComponent(name)}/sessions/${sessionId}`, {
      method: 'DELETE',
    }),
  accountDevices: (name: string, query: { take?: number; offset?: number } = {}) =>
    requestPage<AdminDevice>(`/admin/accounts/${encodeURIComponent(name)}/devices`, query),
  revokeDeviceSessions: (name: string, deviceId: string) =>
    request<void>(
      `/admin/accounts/${encodeURIComponent(name)}/devices/${deviceId}/sessions/revoke`,
      { method: 'POST' },
    ),
  batchRevokeDevices: (name: string, deviceIds: string[]) =>
    request<void>(`/admin/accounts/${encodeURIComponent(name)}/devices/batch/revoke`, {
      method: 'POST',
      body: { device_ids: deviceIds },
    }),
  accountContacts: (name: string) =>
    request<Contact[]>(`/admin/accounts/${encodeURIComponent(name)}/contacts`),
  createContact: (name: string, body: { type: number; content: string }) =>
    request<Contact>(`/admin/accounts/${encodeURIComponent(name)}/contacts`, {
      method: 'POST',
      body,
    }),
  updateContact: (name: string, contactId: string, body: { type?: number; content?: string }) =>
    request<Contact>(
      `/admin/accounts/${encodeURIComponent(name)}/contacts/${contactId}`,
      { method: 'PATCH', body },
    ),
  requestContactVerification: (name: string, contactId: string) =>
    request<void>(
      `/admin/accounts/${encodeURIComponent(name)}/contacts/${contactId}/verify/request`,
      { method: 'POST' },
    ),
  verifyContact: (name: string, contactId: string, body: { verified_at?: string | null } = {}) =>
    request<void>(`/admin/accounts/${encodeURIComponent(name)}/contacts/${contactId}/verify`, {
      method: 'POST',
      body,
    }),
  unverifyContact: (name: string, contactId: string) =>
    request<void>(`/admin/accounts/${encodeURIComponent(name)}/contacts/${contactId}/verify`, {
      method: 'DELETE',
    }),
  setPrimaryContact: (name: string, contactId: string) =>
    request<void>(`/admin/accounts/${encodeURIComponent(name)}/contacts/${contactId}/primary`, {
      method: 'POST',
    }),
  setContactVisibility: (name: string, contactId: string, isPublic: boolean) =>
    request<void>(
      `/admin/accounts/${encodeURIComponent(name)}/contacts/${contactId}/visibility`,
      { method: 'POST', body: { is_public: isPublic } },
    ),
  deleteContact: (name: string, contactId: string) =>
    request<void>(`/admin/accounts/${encodeURIComponent(name)}/contacts/${contactId}`, {
      method: 'DELETE',
    }),
  batchVerifyContacts: (name: string, contactIds: string[]) =>
    request<void>(`/admin/accounts/${encodeURIComponent(name)}/contacts/batch/verify`, {
      method: 'POST',
      body: { contact_ids: contactIds },
    }),
  accountFactors: (name: string) =>
    request<AdminAuthFactor[]>(`/admin/accounts/${encodeURIComponent(name)}/factors`),
  createFactor: (
    name: string,
    body: { type: number; secret?: string; enable?: boolean; code?: string },
  ) =>
    request<AdminAuthFactor>(`/admin/accounts/${encodeURIComponent(name)}/factors`, {
      method: 'POST',
      body,
    }),
  enableFactor: (name: string, factorId: string) =>
    request<AdminAuthFactor>(
      `/admin/accounts/${encodeURIComponent(name)}/factors/${factorId}/enable`,
      { method: 'POST' },
    ),
  disableFactor: (name: string, factorId: string) =>
    request<AdminAuthFactor>(
      `/admin/accounts/${encodeURIComponent(name)}/factors/${factorId}/disable`,
      { method: 'POST' },
    ),
  resetPasswordFactor: (
    name: string,
    body: { new_password: string; revoke_sessions: boolean },
  ) =>
    request<AdminAuthFactor>(
      `/admin/accounts/${encodeURIComponent(name)}/factors/password/reset`,
      { method: 'POST', body },
    ),
  deleteFactor: (name: string, factorId: string) =>
    request<void>(`/admin/accounts/${encodeURIComponent(name)}/factors/${factorId}`, {
      method: 'DELETE',
    }),
  accountSpells: (name: string) =>
    request<MagicSpell[]>(`/admin/accounts/${encodeURIComponent(name)}/spells`),
  createSpell: (
    name: string,
    body: { type: number; meta?: Record<string, unknown>; code?: string; send_email?: boolean },
  ) =>
    request<MagicSpell>(`/admin/accounts/${encodeURIComponent(name)}/spells`, {
      method: 'POST',
      body,
    }),
  resendSpell: (name: string, spellId: string, bypassVerify = false) =>
    request<MagicSpell>(`/admin/accounts/${encodeURIComponent(name)}/spells/${spellId}/resend`, {
      method: 'POST',
      body: { bypass_verify: bypassVerify },
    }),
  deleteSpell: (name: string, spellId: string) =>
    request<void>(`/admin/accounts/${encodeURIComponent(name)}/spells/${spellId}`, {
      method: 'DELETE',
    }),
  accountActionLogs: (
    name: string,
    query: { action?: string; take?: number; offset?: number } = {},
  ) =>
    requestPage<ActionLog>(`/admin/accounts/${encodeURIComponent(name)}/action-logs`, query),
  accountNameHistory: (name: string) =>
    request<NameHistoryEntry[]>(`/admin/accounts/${encodeURIComponent(name)}/name-history`),
  accountConnections: (name: string) =>
    request<Connection[]>(`/admin/accounts/${encodeURIComponent(name)}/connections`),
  accountPasskeys: (name: string) =>
    request<Passkey[]>(`/admin/accounts/${encodeURIComponent(name)}/passkeys`),
  deleteAccountPasskey: (name: string, passkeyId: string) =>
    request<void>(`/admin/accounts/${encodeURIComponent(name)}/passkeys/${passkeyId}`, {
      method: 'DELETE',
    }),
  accountRelationships: (
    name: string,
    query: { status?: number; take?: number; offset?: number } = {},
  ) =>
    requestPage<Relationship>(`/admin/accounts/${encodeURIComponent(name)}/relationships`, query),
  updateAccountProfile: (name: string, body: Partial<Record<keyof Profile, unknown>>) =>
    request<Profile>(`/admin/accounts/${encodeURIComponent(name)}/profile`, {
      method: 'PATCH',
      body,
    }),

  // ── Punishments ──

  punishmentsCreated: (query: { take?: number; offset?: number } = {}) =>
    requestPage<Punishment>('/admin/accounts/punishments/created', query),
  accountPunishments: (name: string, query: { take?: number; offset?: number } = {}) =>
    requestPage<Punishment>(`/accounts/${encodeURIComponent(name)}/punishments`, query),
  createPunishment: (
    name: string,
    body: {
      reason: string
      type: number
      expired_at?: string | null
      blocked_permissions?: string[]
    },
  ) =>
    request<Punishment>(`/admin/accounts/${encodeURIComponent(name)}/punishments`, {
      method: 'POST',
      body,
    }),
  updatePunishment: (
    name: string,
    punishmentId: string,
    body: {
      reason?: string
      type?: number
      expired_at?: string | null
      blocked_permissions?: string[]
    },
  ) =>
    request<Punishment>(
      `/admin/accounts/${encodeURIComponent(name)}/punishments/${punishmentId}`,
      { method: 'PATCH', body },
    ),
  deletePunishment: (name: string, punishmentId: string) =>
    request<void>(
      `/admin/accounts/${encodeURIComponent(name)}/punishments/${punishmentId}`,
      { method: 'DELETE' },
    ),

  // ── Communications ──

  notify: (body: {
    account_id?: string
    account_ids?: string[]
    broadcast_to_all?: boolean
    topic: string
    title?: string
    subtitle?: string
    body?: string
    action_uri?: string
    push_type?: string
    is_silent?: boolean
    is_savable?: boolean
    meta?: Record<string, unknown>
  }) =>
    request<{
      requested: number
      resolved: number
      sent: number
      skipped: number
      broadcast_to_all: boolean
    }>('/admin/accounts/notifications', { method: 'POST', body }),
  sendEmails: (body: {
    account_id?: string
    account_ids?: string[]
    broadcast_to_all?: boolean
    subject: string
    html_body: string
  }) =>
    request<{
      requested: number
      resolved: number
      sent: number
      skipped: number
      broadcast_to_all: boolean
    }>('/admin/accounts/emails', { method: 'POST', body }),
  exportEmails: (query: {
    accountId?: string
    accountIds?: string[]
    broadcastToAll?: boolean
  }) => requestBlob('/admin/accounts/emails/export', query),
  nightOwl: (name: string, hour: number) =>
    request<unknown>(
      `/admin/accounts/${encodeURIComponent(name)}/notifications/night-owl`,
      { method: 'POST', body: { hour } },
    ),

  // ── Account records ──

  badges: (name: string) =>
    request<AdminBadge[]>(`/admin/accounts/${encodeURIComponent(name)}/badges`),
  grantBadge: (
    name: string,
    body: {
      type: string
      label?: string
      caption?: string
      meta?: Record<string, unknown>
      activated_at?: string | null
      expired_at?: string | null
    },
  ) =>
    request<AdminBadge>(`/admin/accounts/${encodeURIComponent(name)}/badges`, {
      method: 'POST',
      body,
    }),
  activateBadge: (name: string, badgeId: string) =>
    request<void>(`/admin/accounts/${encodeURIComponent(name)}/badges/${badgeId}/active`, {
      method: 'POST',
    }),
  revokeBadge: (name: string, badgeId: string) =>
    request<void>(`/admin/accounts/${encodeURIComponent(name)}/badges/${badgeId}`, {
      method: 'DELETE',
    }),
  board: (name: string) =>
    request<AdminBoardItem[]>(`/admin/accounts/${encodeURIComponent(name)}/board`),
  replaceBoard: (name: string, items: Array<Record<string, unknown>>) =>
    request<AdminBoardItem[]>(`/admin/accounts/${encodeURIComponent(name)}/board`, {
      method: 'PUT',
      body: items,
    }),
  updateBoardPayload: (name: string, itemId: string, payload: Record<string, unknown>) =>
    request<AdminBoardItem>(
      `/admin/accounts/${encodeURIComponent(name)}/board/items/${itemId}/payload`,
      { method: 'POST', body: { payload } },
    ),
  deleteBoardItem: (name: string, itemId: string) =>
    request<void>(`/admin/accounts/${encodeURIComponent(name)}/board/items/${itemId}`, {
      method: 'DELETE',
    }),
  setVerification: (name: string, body: VerificationMark) =>
    request<VerificationMark>(`/admin/accounts/${encodeURIComponent(name)}/verification`, {
      method: 'POST',
      body,
    }),
  clearVerification: (name: string) =>
    request<void>(`/admin/accounts/${encodeURIComponent(name)}/verification`, {
      method: 'DELETE',
    }),
  invalidateCredits: (name: string) =>
    request<unknown>(`/admin/accounts/${encodeURIComponent(name)}/credits`, { method: 'POST' }),

  // ── Realms ──

  realms: (
    query: {
      query?: string
      offset?: number
      take?: number
      isPublic?: boolean
      isCommunity?: boolean
      verified?: boolean
      accountId?: string
    } = {},
  ) => requestPage<AdminRealm>('/admin/realms', query),
  realm: (slug: string) =>
    request<AdminRealmDetail>(`/admin/realms/${encodeURIComponent(slug)}`),
  updateRealm: (
    slug: string,
    body: {
      slug?: string
      name?: string
      description?: string
      is_community?: boolean
      is_public?: boolean
      account_id?: string
    },
  ) =>
    request<AdminRealm>(`/admin/realms/${encodeURIComponent(slug)}`, {
      method: 'PATCH',
      body,
    }),
  setRealmVerification: (slug: string, body: VerificationMark) =>
    request<VerificationMark>(`/admin/realms/${encodeURIComponent(slug)}/verification`, {
      method: 'POST',
      body,
    }),
  clearRealmVerification: (slug: string) =>
    request<void>(`/admin/realms/${encodeURIComponent(slug)}/verification`, {
      method: 'DELETE',
    }),
  realmMembers: (
    slug: string,
    query: { pendingOnly?: boolean; role?: number; offset?: number; take?: number } = {},
  ) => requestPage<RealmMember>(`/admin/realms/${encodeURIComponent(slug)}/members`, query),
  updateRealmMemberRole: (slug: string, memberId: string, role: number) =>
    request<RealmMember>(
      `/admin/realms/${encodeURIComponent(slug)}/members/${memberId}/role`,
      { method: 'PATCH', body: { role } },
    ),
  removeRealmMember: (slug: string, memberId: string) =>
    request<void>(`/admin/realms/${encodeURIComponent(slug)}/members/${memberId}`, {
      method: 'DELETE',
    }),
  deleteRealm: (slug: string) =>
    request<void>(`/admin/realms/${encodeURIComponent(slug)}`, { method: 'DELETE' }),

  // ── Tickets ──

  onCall: () => request<OnCallAdmin[]>('/admin/tickets/on-call'),
  addOnCall: (accountId: string) =>
    request<OnCallAdmin>('/admin/tickets/on-call', {
      method: 'POST',
      body: { account_id: accountId },
    }),
  removeOnCall: (accountId: string) =>
    request<void>(`/admin/tickets/on-call/${accountId}`, { method: 'DELETE' }),

  // ── Progression ──

  achievements: () => request<ProgressionDefinition[]>('/admin/progression/achievements'),
  createAchievement: (body: ProgressionUpsert) =>
    request<ProgressionDefinition>('/admin/progression/achievements', {
      method: 'POST',
      body,
    }),
  updateAchievement: (identifier: string, body: ProgressionUpsert) =>
    request<ProgressionDefinition>(
      `/admin/progression/achievements/${encodeURIComponent(identifier)}`,
      { method: 'PUT', body },
    ),
  toggleAchievement: (identifier: string) =>
    request<ProgressionDefinition>(
      `/admin/progression/achievements/${encodeURIComponent(identifier)}/enable`,
      { method: 'POST' },
    ),
  quests: () => request<ProgressionDefinition[]>('/admin/progression/quests'),
  createQuest: (body: ProgressionUpsert) =>
    request<ProgressionDefinition>('/admin/progression/quests', { method: 'POST', body }),
  updateQuest: (identifier: string, body: ProgressionUpsert) =>
    request<ProgressionDefinition>(`/admin/progression/quests/${encodeURIComponent(identifier)}`, {
      method: 'PUT',
      body,
    }),
  toggleQuest: (identifier: string) =>
    request<ProgressionDefinition>(
      `/admin/progression/quests/${encodeURIComponent(identifier)}/enable`,
      { method: 'POST' },
    ),
  syncProgression: () => request<unknown>('/admin/progression/sync', { method: 'POST' }),
  testProgressionPacket: (body: Record<string, unknown> = {}) =>
    request<unknown>('/admin/progression/test-ws-packet', { method: 'POST', body }),

  // ── Wallet products ──

  walletProduct: () =>
    request<WalletProductSummary>('/admin/wallet-products/golds-resupply-pack'),
  applyWalletOrder: (orderId: string) =>
    request<WalletOrder>(
      `/admin/wallet-products/orders/${encodeURIComponent(orderId)}/apply`,
      { method: 'POST' },
    ),
}
