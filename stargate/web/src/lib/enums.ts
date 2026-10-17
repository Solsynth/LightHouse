/** Display labels for the numeric enums the API emits. */

export const factorTypeLabels: Record<number, string> = {
  0: 'password',
  1: 'email code',
  2: 'in-app approval',
  3: 'authenticator',
  4: 'PIN',
  5: 'recovery code',
  6: 'NFC tag',
  7: 'passkey',
  8: 'QR login',
}

export const factorTypeDescriptions: Record<number, string> = {
  0: 'A password you type at sign-in.',
  1: 'A six-digit code sent to a verified email.',
  2: 'Approve the sign-in from another signed-in device.',
  3: 'A rotating code from an authenticator app.',
  4: 'A short PIN you choose.',
  5: 'A one-time code that restores access.',
  6: 'A physical NFC tag registered to this account.',
  7: 'A credential held by this device, unlocked biometrically.',
  8: 'Scan a code from a signed-in device.',
}

export const platformLabels: Record<number, string> = {
  0: 'unidentified',
  1: 'web',
  2: 'iOS',
  3: 'Android',
  4: 'macOS',
  5: 'Windows',
  6: 'Linux',
}

export const sessionTypeLabels: Record<number, string> = {
  0: 'login',
  1: 'oauth',
  2: 'oidc',
  3: 'api key',
}

export const contactTypeLabels: Record<number, string> = {
  0: 'email',
  1: 'phone',
  2: 'address',
}

export const punishmentTypeLabels: Record<number, string> = {
  0: 'permission change',
  1: 'sign-in blocked',
  2: 'account disabled',
  3: 'strike',
}

/** Permission-node actor types stored by the server (group = 1). */
export const actorTypeLabels: Record<number, string> = {
  0: 'account',
  1: 'group',
}

/**
 * Factors a person can pick to satisfy a sign-in challenge. Passkeys use the
 * dedicated assertion endpoints; in-app and QR approval never appear here.
 */
export const pickableFactorTypes: Record<number, true> = {
  0: true,
  1: true,
  3: true,
  4: true,
  6: true,
}

/** Factors that need a code issued before the credential is submitted. */
export const codeRequestFactorTypes: Record<number, true> = {
  1: true,
}

export const securityModeCopy: Record<string, string> = {
  default: 'Risk-based. The service asks for extra factors when a sign-in looks unfamiliar.',
  lockdown: 'Always ask for every enabled factor.',
  lockoff: 'Ask for a single factor, even when the sign-in looks unfamiliar.',
}
