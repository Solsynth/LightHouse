import { afterEach, describe, expect, it, vi } from 'vitest'

import { pickFiles } from '@/lib/picker'
import type { PickerFile } from '@/lib/picker-types'

function picked(overrides: Partial<PickerFile> = {}): PickerFile {
  return {
    id: 'file-1',
    name: 'cat.png',
    is_folder: false,
    mime_type: 'image/png',
    size: 10,
    extension: 'png',
    kind: 'image',
    parent_id: null,
    created_at: '',
    updated_at: '',
    url: '/api/files/file-1/open',
    thumbnail_url: '/api/files/file-1?thumbnail=1',
    file: {},
    ...overrides,
  }
}

afterEach(() => {
  delete window.openSnfsPicker
})

describe('pickFiles', () => {
  it('maps frame-relative paths onto the gateway drive route', async () => {
    window.openSnfsPicker = vi.fn<() => Promise<PickerFile[]>>(async () => [picked()])

    const files = await pickFiles({ title: 'Attach' })

    expect(files[0]?.url).toMatch(/\/drive\/files\/file-1$/)
    expect(files[0]?.thumbnail_url).toMatch(/\/drive\/files\/file-1\?thumbnail=true$/)
  })

  it('keeps a missing thumbnail missing', async () => {
    window.openSnfsPicker = vi.fn<() => Promise<PickerFile[]>>(async () => [
      picked({ thumbnail_url: null }),
    ])

    const files = await pickFiles({ title: 'Attach' })

    expect(files[0]?.thumbnail_url).toBeNull()
  })
})
