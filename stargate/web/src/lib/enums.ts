import { i18n, translate } from '@/lib/i18n'

/** Display labels for the numeric enums the API emits. */

function enumLabel(group: string, value: number): string {
  const key = `enums.${group}.${value}`
  return i18n.global.te(key) ? translate(key) : String(value)
}

export const factorTypeLabel = (type: number) => enumLabel('factor', type)
export const factorTypeDescription = (type: number) => enumLabel('factorDescription', type)
export const platformLabel = (type: number) => enumLabel('platform', type)
export const sessionTypeLabel = (type: number) => enumLabel('sessionType', type)
export const contactTypeLabel = (type: number) => enumLabel('contactType', type)
export const punishmentTypeLabel = (type: number) => enumLabel('punishmentType', type)
export const actorTypeLabel = (type: number) => enumLabel('actorType', type)

export function securityModeCopy(mode: string): string {
  const key = `enums.securityMode.${mode}`
  return i18n.global.te(key) ? translate(key) : mode
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
