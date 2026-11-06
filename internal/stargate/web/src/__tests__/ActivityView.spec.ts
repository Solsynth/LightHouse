import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import ActivityView from '@/views/ActivityView.vue'
import { activityApi } from '@/lib/api'
import { i18n } from '@/lib/i18n'
import type { ActionLogSummary } from '@/lib/api/types'

vi.mock('@/lib/api', () => ({
  activityApi: { summary: vi.fn() },
}))

const summaryMock = vi.mocked(activityApi.summary)

function summary(overrides: Partial<ActionLogSummary> = {}): ActionLogSummary {
  return {
    days: 30,
    since: '2026-10-07T00:00:00Z',
    until: '2026-11-06T00:00:00Z',
    total: 40,
    last_24h: 2,
    last_7d: 9,
    last_30d: 20,
    groups: [
      { action: 'login', count: 12, last_at: '2026-11-05T10:00:00Z' },
      { action: 'accounts.profile.update', count: 3, last_at: '2026-11-04T10:00:00Z' },
    ],
    buckets: [
      { start: '2026-11-04T00:00:00Z', count: 4 },
      { start: '2026-11-05T00:00:00Z', count: 2 },
    ],
    ...overrides,
  }
}

describe('ActivityView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    summaryMock.mockResolvedValue(summary())
  })

  it('renders grouped counts and daily volume instead of record rows', async () => {
    const wrapper = mount(ActivityView, { global: { plugins: [i18n] } })
    await flushPromises()

    expect(summaryMock).toHaveBeenCalledWith({ days: 30 })
    expect(wrapper.text()).toContain('Signed in')
    expect(wrapper.text()).toContain('12')
    expect(wrapper.text()).toContain('Profile updated')
    expect(wrapper.text()).toContain('3')
    expect(wrapper.text()).toContain('Daily Volume')
    expect(wrapper.find('table.ledger').exists()).toBe(false)
  })

  it('reloads the summary when the window changes', async () => {
    const wrapper = mount(ActivityView, { global: { plugins: [i18n] } })
    await flushPromises()

    const sevenDays = wrapper.findAll('button').find((button) => button.text() === '7 days')
    expect(sevenDays).toBeDefined()
    await sevenDays!.trigger('click')
    await flushPromises()

    expect(summaryMock).toHaveBeenLastCalledWith({ days: 7 })
  })
})
