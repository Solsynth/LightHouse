const DEVICE_KEY = 'stargate.device'

interface DeviceIdentity {
  id: string
  name: string
}

function browserName(): string {
  const ua = navigator.userAgent
  if (/Firefox\//.test(ua)) return 'Firefox'
  if (/Edg\//.test(ua)) return 'Edge'
  if (/Chrome\//.test(ua)) return 'Chrome'
  if (/Safari\//.test(ua)) return 'Safari'
  return 'Browser'
}

function platformName(): string {
  const ua = navigator.userAgent
  if (/Mac OS X/.test(ua)) return 'macOS'
  if (/Windows/.test(ua)) return 'Windows'
  if (/Android/.test(ua)) return 'Android'
  if (/iPhone|iPad/.test(ua)) return 'iOS'
  if (/Linux/.test(ua)) return 'Linux'
  return 'web'
}

/**
 * Stable identity for this browser, so challenges, devices and sessions can
 * be told apart. Kept in localStorage because the server has no cookie for it.
 */
export function deviceIdentity(): DeviceIdentity {
  const fallback = { id: crypto.randomUUID(), name: `${browserName()} on ${platformName()}` }
  try {
    const raw = localStorage.getItem(DEVICE_KEY)
    if (raw) {
      const parsed = JSON.parse(raw) as Partial<DeviceIdentity>
      if (parsed.id && parsed.name) return { id: parsed.id, name: parsed.name }
    }
    localStorage.setItem(DEVICE_KEY, JSON.stringify(fallback))
  } catch {
    // Private mode: a per-load id still works for a single challenge.
  }
  return fallback
}
