import { describe, expect, it } from 'vitest'

import { resolveFileUrl } from '@/lib/media'

describe('resolveFileUrl', () => {
  it('derives the drive route from an id-only reference', () => {
    expect(resolveFileUrl({ id: 'file-1' })).toMatch(/\/drive\/files\/file-1$/)
    expect(resolveFileUrl({ id: 'file-1' }, 'thumbnail')).toMatch(
      /\/drive\/files\/file-1\?thumbnail=true$/,
    )
  })

  it('prefers a URL the reference already carries', () => {
    expect(resolveFileUrl({ id: 'file-1', url: 'https://files.example/cat.png' })).toBe(
      'https://files.example/cat.png',
    )
  })
})
