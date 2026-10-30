import { currentLocale, translate } from '@/lib/i18n'

const minute = 60_000
const hour = 60 * minute
const day = 24 * hour

/** Absolute UTC stamp in the format the server logs use. */
export function utcStamp(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return `${date.toISOString().slice(0, 19).replace('T', ' ')}Z`
}

/** Compact age: 42s, 7m, 3h, 12d. */
export function age(value?: string): string {
  if (!value) return '—'
  const ms = Date.now() - new Date(value).getTime()
  if (Number.isNaN(ms)) return '—'
  if (ms < 0) return translate('time.pending')
  if (ms < minute) return `${Math.max(1, Math.floor(ms / 1000))}s`
  if (ms < hour) return `${Math.floor(ms / minute)}m`
  if (ms < day) return `${Math.floor(ms / hour)}h`
  return `${Math.floor(ms / day)}d`
}

/** Remaining lifetime: 4h 12m, 18d, expired. */
export function remaining(value?: string): string {
  if (!value) return translate('time.noExpiry')
  const ms = new Date(value).getTime() - Date.now()
  if (Number.isNaN(ms)) return '—'
  if (ms <= 0) return translate('time.expired')
  if (ms < hour) return `${Math.max(1, Math.floor(ms / minute))}m`
  if (ms < day) {
    const hours = Math.floor(ms / hour)
    const minutes = Math.floor((ms % hour) / minute)
    return minutes ? `${hours}h ${minutes}m` : `${hours}h`
  }
  return `${Math.floor(ms / day)}d`
}

export function relative(value?: string): string {
  if (!value) return '—'
  const ms = Date.now() - new Date(value).getTime()
  if (Number.isNaN(ms)) return '—'
  if (ms < minute) return translate('time.justNow')
  if (ms < hour) return translate('time.minutesAgo', { n: Math.floor(ms / minute) })
  if (ms < day) return translate('time.hoursAgo', { n: Math.floor(ms / hour) })
  if (ms < 30 * day) return translate('time.daysAgo', { n: Math.floor(ms / day) })
  return utcStamp(value).slice(0, 10)
}

export function bytes(value?: number): string {
  if (!value) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let size = value
  let unit = 0
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024
    unit += 1
  }
  return `${size.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}

export function compactNumber(value?: number): string {
  if (value === undefined || value === null) return '—'
  return new Intl.NumberFormat(currentLocale(), {
    notation: 'compact',
    maximumFractionDigits: 1,
  }).format(value)
}

/** Short id for dense ledgers; full value stays available via title. */
export function shortId(value?: string): string {
  if (!value) return '—'
  return value.length > 8 ? value.slice(0, 8) : value
}

/** Parses a user-agent string into something a person can scan. */
export function describeAgent(value?: string): string {
  if (!value) return translate('time.unknownClient')
  const browser = /Firefox\/(\d+)/.exec(value)
  if (browser) return `Firefox ${browser[1]}`
  const edge = /Edg\/(\d+)/.exec(value)
  if (edge) return `Edge ${edge[1]}`
  const chrome = /Chrome\/(\d+)/.exec(value)
  if (chrome) return `Chrome ${chrome[1]}`
  const safari = /Version\/(\d+).*Safari/.exec(value)
  if (safari) return `Safari ${safari[1]}`
  const curl = /curl\/(\S+)/.exec(value)
  if (curl) return `curl ${curl[1]}`
  return value.length > 42 ? `${value.slice(0, 42)}…` : value
}

export function place(point?: { city?: string; country?: string; country_code?: string }): string {
  if (!point) return '—'
  const parts = [point.city, point.country || point.country_code].filter(Boolean)
  return parts.length ? parts.join(', ') : '—'
}
