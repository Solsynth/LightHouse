import { request, requestForm, requestPage, sendString } from './client'
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
  GeographyBucket,
  PasskeyLoginStart,
  PermissionGroup,
  PermissionGroupDetail,
  PermissionMember,
  PermissionNode,
  Profile,
  Punishment,
} from './types'

export type { Page }

// ── Account ──

export const accountApi = {
  me: () => request<Account>('/accounts/me'),
  updateBasicInfo: (body: { nick?: string; language?: string; region?: string }) =>
    request<Account>('/accounts/me', { method: 'PATCH', body }),
  updateProfile: (body: Partial<Record<keyof Profile, unknown>>) =>
    request<Profile>('/accounts/me/profile', { method: 'PATCH', body }),
  requestDeletion: () => request<void>('/accounts/me', { method: 'DELETE' }),
  search: (query: string, take = 20) =>
    request<Account[]>('/accounts/search', { query: { query, take } }),
}

// ── Auth ──

export const authApi = {
  captchaConfig: () => request<CaptchaConfig>('/auth/captcha'),
  providers: () => request<string[]>('/auth/providers'),
  authorizeInfo: (query: Record<string, string>) =>
    request<AuthorizeClientInfo>('/auth/open/authorize', { query }),
  authorizeDecision: (form: URLSearchParams) =>
    requestForm<AuthorizeDecision>('/auth/open/authorize', form),
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
  passkeyDiscoverableStart: (body: {
    device_id: string
    device_name: string
    platform: number
  }) => request<PasskeyLoginStart>('/auth/passkey/start', { method: 'POST', body }),
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
  applySpell: (word: string) =>
    request<unknown>(`/spells/${encodeURIComponent(word)}/apply`, { method: 'POST' }),
}

// ── Security ──

export const securityApi = {
  factors: () => request<AuthFactor[]>('/factors'),
  createFactor: (type: number, secret?: string) =>
    request<AuthFactor>('/factors', { method: 'POST', body: { type, secret } }),
  enableFactor: (id: string, code?: string) =>
    request<AuthFactor>(`/factors/${id}/enable`, { method: 'POST', body: code ?? undefined }),
  disableFactor: (id: string) =>
    request<AuthFactor>(`/factors/${id}/disable`, { method: 'POST' }),
  deleteFactor: (id: string) => request<void>(`/factors/${id}`, { method: 'DELETE' }),
  passkeys: () => request<Array<{ id: string; label: string; created_at?: string }>>('/factors/passkey'),
  updatePasskey: (id: string, label: string) =>
    request<unknown>(`/factors/passkey/${id}`, { method: 'PATCH', body: { label } }),
  deletePasskey: (id: string) => request<void>(`/factors/passkey/${id}`, { method: 'DELETE' }),
  contacts: () => request<Contact[]>('/contacts'),
  createContact: (type: number, content: string) =>
    request<Contact>('/contacts', { method: 'POST', body: { type, content } }),
  verifyContact: (id: string) => request<Contact>(`/contacts/${id}/verify`, { method: 'POST' }),
  setPrimaryContact: (id: string) => request<Contact>(`/contacts/${id}/primary`, { method: 'POST' }),
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
  deleteDevice: (id: string) => request<void>(`/devices/${id}`, { method: 'DELETE' }),
  renameDevice: (id: string, label: string) => sendString(`/devices/${id}/label`, label),
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
    request<GeographyBucket[]>('/admin/stats/users/geography', {
      query: { precision, since },
    }),
}
