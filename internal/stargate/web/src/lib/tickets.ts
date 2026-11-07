import type { InjectionKey } from 'vue'

/**
 * The ticket wire enums and the console's visual signals for them. Numeric
 * values are the server contract; labels come from the locale catalogs, so a
 * new status is never hidden by a missing translation.
 */

export const TICKET_TYPES = [0, 1, 2, 3, 4] as const
export const TICKET_STATUSES = [0, 1, 2, 3, 4, 5] as const
export const TICKET_PRIORITIES = [0, 1, 2, 3] as const

export const TICKET_STATUS = {
  open: 0,
  inProgress: 1,
  resolved: 2,
  closed: 3,
  waitingForCustomer: 4,
  waitingForInformation: 5,
} as const

export const TICKET_TYPE = {
  support: 0,
  bugReport: 1,
  featureRequest: 2,
  billing: 3,
  other: 4,
} as const

export const TICKET_PRIORITY = {
  low: 0,
  medium: 1,
  high: 2,
  critical: 3,
} as const

/** The two states that no longer need an answer. */
export function isSettled(status: number): boolean {
  return status === TICKET_STATUS.resolved || status === TICKET_STATUS.closed
}

/**
 * A status reads through a rail, a dot, and a label. The rail is a thin bar
 * beside a queue row or the detail header; the dot repeats the same tone where
 * the rail has no room. Text always carries the meaning.
 */
export function statusRail(status: number): string {
  switch (status) {
    case TICKET_STATUS.open:
      return 'bg-primary'
    case TICKET_STATUS.inProgress:
      return 'bg-warning'
    case TICKET_STATUS.resolved:
      return 'bg-success'
    case TICKET_STATUS.closed:
      return 'bg-line'
    case TICKET_STATUS.waitingForCustomer:
      return 'bg-accent'
    case TICKET_STATUS.waitingForInformation:
      return 'bg-info'
    default:
      return 'bg-faint'
  }
}

export function statusText(status: number): string {
  switch (status) {
    case TICKET_STATUS.open:
      return 'text-primary'
    case TICKET_STATUS.inProgress:
      return 'text-warning'
    case TICKET_STATUS.resolved:
      return 'text-success'
    case TICKET_STATUS.closed:
      return 'text-faint'
    case TICKET_STATUS.waitingForCustomer:
      return 'text-accent'
    case TICKET_STATUS.waitingForInformation:
      return 'text-info'
    default:
      return 'text-dim'
  }
}

/** Priority is ordinal, so it is drawn as a fill count rather than a colour. */
export function prioritySignal(priority: number): string {
  if (priority >= TICKET_PRIORITY.critical) return 'bg-error'
  if (priority >= TICKET_PRIORITY.high) return 'bg-warning'
  if (priority >= TICKET_PRIORITY.medium) return 'bg-info'
  return 'bg-faint'
}

export function priorityText(priority: number): string {
  if (priority >= TICKET_PRIORITY.critical) return 'text-error'
  if (priority >= TICKET_PRIORITY.high) return 'text-warning'
  if (priority >= TICKET_PRIORITY.medium) return 'text-info'
  return 'text-faint'
}

/** Provided by the support desk so a child can refresh the queue it sits beside. */
export const TICKET_REFRESH = Symbol('ticket-refresh') as InjectionKey<() => Promise<void>>
