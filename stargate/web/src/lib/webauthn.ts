/**
 * WebAuthn glue. The server speaks base64url for every binary field and
 * expects the raw assertion parts separately, so each ceremony is converted
 * here rather than in the views.
 */

function toB64(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer)
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '')
}

function fromBase64Url(value: string): Uint8Array<ArrayBuffer> {
  const normalized = value.replaceAll('-', '+').replaceAll('_', '/')
  const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, '=')
  const binary = atob(padded)
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index)
  return bytes
}

export interface AssertionParts {
  credential_id: string
  client_data_json: string
  authenticator_data: string
  signature: string
  user_handle?: string
}

/**
 * Runs a webauthn.get ceremony against the challenge the server issued.
 * `allowCredentials` empty means a discoverable credential picker.
 */
export async function getAssertion(options: {
  challenge: string
  rpId: string
  allowCredentials: Array<{ type: string; id: string }>
  timeout?: number
  userVerification?: string
}): Promise<AssertionParts> {
  const publicKey: PublicKeyCredentialRequestOptions = {
    challenge: fromBase64Url(options.challenge),
    rpId: options.rpId,
    allowCredentials: options.allowCredentials.map((credential) => ({
      type: 'public-key' as const,
      id: fromBase64Url(credential.id),
    })),
    timeout: options.timeout ?? 60_000,
    userVerification: (options.userVerification ?? 'preferred') as UserVerificationRequirement,
  }

  const credential = (await navigator.credentials.get({ publicKey })) as PublicKeyCredential | null
  if (!credential) throw new Error('No passkey was selected.')

  const response = credential.response as AuthenticatorAssertionResponse
  const parts: AssertionParts = {
    credential_id: toB64(credential.rawId),
    client_data_json: toB64(response.clientDataJSON),
    authenticator_data: toB64(response.authenticatorData),
    signature: toB64(response.signature),
  }
  if (response.userHandle) parts.user_handle = toB64(response.userHandle)
  return parts
}

/** True when this browser can run a WebAuthn ceremony at all. */
export function passkeysSupported(): boolean {
  return typeof window.PublicKeyCredential !== 'undefined'
}

/** Human-readable failure for the errors the platform raises. */
export function passkeyFailure(error: unknown): string {
  if (error instanceof DOMException) {
    if (error.name === 'NotAllowedError') return 'The passkey prompt was dismissed or timed out.'
    if (error.name === 'SecurityError') return 'This origin is not allowed to use passkeys.'
    if (error.name === 'InvalidStateError') return 'That passkey is already registered here.'
    return error.message
  }
  return error instanceof Error ? error.message : 'The passkey ceremony failed.'
}
