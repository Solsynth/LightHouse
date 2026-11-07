import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import type { Account } from '@/lib/api/types'
import AccountAvatar from '@/components/AccountAvatar.vue'

function account(overrides: Partial<Account> = {}): Account {
  return {
    id: 'account-1',
    name: 'alice',
    nick: 'Alice',
    language: 'en',
    region: 'us',
    is_superuser: false,
    perk_level: 0,
    ...overrides,
  }
}

describe('AccountAvatar', () => {
  it('uses the canonical redirect for a profile picture with only a drive id', () => {
    const wrapper = mount(AccountAvatar, {
      props: {
        account: account({
          profile: {
            id: 'profile-1',
            account_id: 'account-1',
            experience: 0,
            level: 0,
            leveling_progress: 0,
            social_credits: 0,
            social_credits_level: 0,
            picture: { id: 'file-1' },
          },
        }),
      },
    })

    expect(wrapper.get('img').attributes('src')).toBe('/api/accounts/alice/picture')
  })

  it('uses the profile URL when the reference carries one', () => {
    const wrapper = mount(AccountAvatar, {
      props: {
        account: account({
          profile: {
            id: 'profile-1',
            account_id: 'account-1',
            experience: 0,
            level: 0,
            leveling_progress: 0,
            social_credits: 0,
            social_credits_level: 0,
            picture: { id: 'file-1', url: 'https://files.example/picture.png' },
          },
        }),
      },
    })

    expect(wrapper.get('img').attributes('src')).toBe('https://files.example/picture.png')
  })

  it('does not probe the canonical endpoint for a profile without a picture', () => {
    const wrapper = mount(AccountAvatar, {
      props: {
        account: account({
          profile: {
            id: 'profile-1',
            account_id: 'account-1',
            experience: 0,
            level: 0,
            leveling_progress: 0,
            social_credits: 0,
            social_credits_level: 0,
          },
        }),
      },
    })

    expect(wrapper.find('img').exists()).toBe(false)
  })

  it('falls back to the canonical endpoint for profile-less payloads', () => {
    const wrapper = mount(AccountAvatar, { props: { account: account() } })

    expect(wrapper.get('img').attributes('src')).toBe('/api/accounts/alice/picture')
  })
})
