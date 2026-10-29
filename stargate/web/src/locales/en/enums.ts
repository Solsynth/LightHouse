export default {
  enums: {
    factor: {
      0: 'password',
      1: 'email code',
      2: 'in-app approval',
      3: 'authenticator',
      4: 'PIN',
      5: 'recovery code',
      6: 'NFC tag',
      7: 'passkey',
      8: 'QR login',
    },
    factorDescription: {
      0: 'A password you type at sign-in.',
      1: 'A six-digit code sent to a verified email.',
      2: 'Approve the sign-in from another signed-in device.',
      3: 'A rotating code from an authenticator app.',
      4: 'A short PIN you choose.',
      5: 'A one-time code that restores access.',
      6: 'A physical NFC tag registered to this account.',
      7: 'A passkey on this device, unlocked with your fingerprint or face.',
      8: 'Scan a code from a signed-in device.',
    },
    platform: {
      0: 'unidentified',
      1: 'web',
      2: 'iOS',
      3: 'Android',
      4: 'macOS',
      5: 'Windows',
      6: 'Linux',
    },
    sessionType: {
      0: 'login',
      1: 'oauth',
      2: 'oidc',
      3: 'api key',
    },
    contactType: {
      0: 'email',
      1: 'phone',
      2: 'address',
    },
    punishmentType: {
      0: 'permission change',
      1: 'sign-in blocked',
      2: 'account disabled',
      3: 'strike',
    },
    actorType: {
      0: 'account',
      1: 'group',
    },
    // CustomAppStatus: only a production client enforces its redirect list.
    oauthAppStatus: {
      0: 'developing',
      1: 'staging',
      2: 'production',
      3: 'suspended',
    },
    securityMode: {
      default: 'Risk-based. The service asks for extra factors when a sign-in looks unfamiliar.',
      lockdown: 'Always ask for every enabled factor.',
      lockoff: 'Ask for a single factor, even when the sign-in looks unfamiliar.',
    },
  },
}
