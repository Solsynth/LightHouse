/**
 * Browsing state for one picker surface.
 *
 * This is deliberately not the drive's `vault` store: a picker may be open
 * beside the drive, and it must not move the drive's folder, filters, or
 * selection. Each mount gets its own instance, so two pickers on one page are
 * independent too.
 *
 * Two sources share this state: the indexed drive tree and the account's
 * unindexed uploads. Visited folders are kept in a small local cache, so
 * walking back up a trail paints from memory instead of asking again; the same
 * cache is what makes a reused frame reopen on the last folder instantly.
 */

import { computed, ref } from 'vue'

import { DriveError } from '@/lib/api/client'
import { driveApi } from '@/lib/api/drive'
import type { CloudFile } from '@/lib/api/types'
import { translate } from '@/lib/i18n'
import { isSelectable, listingFilters } from './filtering'
import type { PickerOptions } from './protocol'

export interface PickerCrumb {
  id: string | null
  name: string
}

/** Which collection the picker is listing. */
export type PickerSource = 'drive' | 'unindexed'

interface CachedPage {
  items: CloudFile[]
  total: number
  at: number
}

const PAGE_SIZE = 100
const CACHE_LIMIT = 24
/** Past this age a painted page is refreshed in the background. */
const CACHE_TTL = 5_000

function errorMessage(caught: unknown): string {
  if (caught instanceof DriveError) {
    if (caught.status === 403) return translate('picker.errors.forbidden')
    if (caught.status === 404) return translate('picker.errors.missing')
    if (caught.status === 0) return caught.message
    return caught.message
  }
  if (caught instanceof Error) return caught.message
  return translate('picker.errors.unknown')
}

export function usePickerBrowser(options: () => PickerOptions) {
  const source = ref<PickerSource>('drive')
  const folderId = ref<string | null>(options().startFolderId ?? null)
  const rootCrumb = (): PickerCrumb[] => [{ id: null, name: translate('picker.root') }]
  const crumbs = ref<PickerCrumb[]>(rootCrumb())
  const items = ref<CloudFile[]>([])
  const total = ref(0)
  const loading = ref(false)
  const loadingMore = ref(false)
  const error = ref('')
  const query = ref('')

  /** The drive folder and trail survive a trip to the unindexed tab. */
  let driveFolder = folderId.value
  let driveCrumbs = crumbs.value
  let loadSeq = 0
  let searchTimer: number | undefined

  const cache = new Map<string, CachedPage>()
  const cacheLoads = new Map<string, Promise<CachedPage>>()
  let cacheGeneration = 0

  const hasMore = computed(() => items.value.length < total.value)
  const selectableCount = computed(
    () => items.value.filter((item) => isSelectable(item, options())).length,
  )
  const isEmpty = computed(() => !loading.value && !error.value && items.value.length === 0)

  /** The unindexed source is a flat list; folders cannot exist there. */
  function cacheKey(): string | null {
    if (query.value.trim()) return null
    const folder = source.value === 'drive' ? (folderId.value ?? '') : ''
    return `${source.value}:${folder}:${options().view ?? 'files'}`
  }

  function remember(key: string, page: CachedPage) {
    cache.delete(key)
    cache.set(key, page)
    while (cache.size > CACHE_LIMIT) {
      const oldest = cache.keys().next().value
      if (oldest === undefined) break
      cache.delete(oldest)
    }
  }

  function cached(key: string): CachedPage | undefined {
    const hit = cache.get(key)
    if (!hit) return undefined
    cache.delete(key)
    cache.set(key, hit)
    return hit
  }

  function requestFirst(key: string): Promise<CachedPage> {
    const pending = cacheLoads.get(key)
    if (pending) return pending
    const generation = cacheGeneration
    const request = fetchPage(0).then((page) => {
      const cachedPage = { items: page.items, total: page.total, at: Date.now() }
      if (generation === cacheGeneration) remember(key, cachedPage)
      return cachedPage
    })
    cacheLoads.set(key, request)
    request.then(
      () => cacheLoads.delete(key),
      () => cacheLoads.delete(key),
    )
    return request
  }

  async function fetchPage(offset: number) {
    const listing = {
      ...listingFilters(options()),
      offset,
      take: PAGE_SIZE,
      query: query.value.trim() || undefined,
      order: 'name' as const,
      orderDesc: false,
      recycled: false,
    }
    if (source.value === 'unindexed') return driveApi.unindexed(listing)
    return folderId.value ? driveApi.children(folderId.value, listing) : driveApi.root(listing)
  }

  async function load(append = false, force = false) {
    const seq = ++loadSeq
    if (append) loadingMore.value = true
    else loading.value = true
    if (!append) error.value = ''

    const key = cacheKey()
    try {
      if (!append && key && !force) {
        const hit = cached(key)
        if (hit) {
          items.value = hit.items
          total.value = hit.total
          loading.value = false
          if (Date.now() - hit.at >= CACHE_TTL) {
            void revalidate(key, seq, hit.items.length)
          }
          return
        }
      }

      const offset = append ? items.value.length : 0
      const page =
        !append && key && !force
          ? await requestFirst(key)
          : await fetchPage(offset).then((listing) => ({
              items: listing.items,
              total: listing.total,
              at: Date.now(),
            }))
      if (seq !== loadSeq) return
      if (append) {
        items.value = [...items.value, ...page.items]
        total.value = page.total
      } else {
        items.value = page.items
        total.value = page.total
        if (key) remember(key, page)
      }
    } catch (caught) {
      if (seq !== loadSeq) return
      if (caught instanceof DOMException && caught.name === 'AbortError') return
      error.value = errorMessage(caught)
      if (!append) {
        items.value = []
        total.value = 0
      }
    } finally {
      if (seq === loadSeq) {
        loading.value = false
        loadingMore.value = false
      }
    }
  }

  /** Repaints an already visible page from newer data, unless it moved on. */
  async function revalidate(key: string, seq: number, baseLength: number) {
    try {
      const page = await requestFirst(key)
      if (seq !== loadSeq || cacheKey() !== key) return
      if (items.value.length > baseLength) return
      items.value = page.items
      total.value = page.total
    } catch {
      // The painted page stays; a failed background refresh is not an error.
    }
  }

  /** Opens one folder, resolving its breadcrumb chain while the listing loads. */
  async function enter(id: string | null) {
    if (source.value !== 'drive') return
    folderId.value = id
    driveFolder = id
    query.value = ''
    if (id === null) {
      crumbs.value = rootCrumb()
      driveCrumbs = crumbs.value
      await load()
      return
    }

    const existing = crumbs.value.findIndex((crumb) => crumb.id === id)
    if (existing >= 0) crumbs.value = crumbs.value.slice(0, existing + 1)
    driveCrumbs = crumbs.value

    const chainPromise = driveApi.breadcrumb(id).catch(() => null)
    await load()
    const chain = await chainPromise
    if (!chain || source.value !== 'drive' || folderId.value !== id) return
    crumbs.value = [...rootCrumb(), ...chain.map((crumb) => ({ id: crumb.id, name: crumb.name }))]
    driveCrumbs = crumbs.value
  }

  /** Switches between the indexed tree and the unindexed collection. */
  function setSource(next: PickerSource) {
    if (next === source.value) return
    if (next === 'unindexed' && options().view === 'folders') return
    if (source.value === 'drive') {
      driveFolder = folderId.value
      driveCrumbs = crumbs.value
    }
    source.value = next
    query.value = ''
    error.value = ''
    items.value = []
    total.value = 0
    if (next === 'unindexed') {
      folderId.value = null
    } else {
      folderId.value = driveFolder
      crumbs.value = driveCrumbs
    }
    void load()
  }

  /** A new request from the host starts from the requested folder, not the last one. */
  function reset() {
    source.value = 'drive'
    folderId.value = options().startFolderId ?? null
    driveFolder = folderId.value
    crumbs.value = rootCrumb()
    driveCrumbs = crumbs.value
    query.value = ''
    items.value = []
    total.value = 0
    error.value = ''
    loading.value = false
    loadingMore.value = false
    cache.clear()
    cacheGeneration += 1
    void enter(folderId.value)
  }

  function jumpTo(index: number) {
    const crumb = crumbs.value[index]
    if (!crumb || crumb.id === folderId.value) return
    void enter(crumb.id)
  }

  function search(value: string) {
    query.value = value
    if (searchTimer) window.clearTimeout(searchTimer)
    searchTimer = window.setTimeout(() => {
      void load()
    }, 240)
  }

  void enter(folderId.value)

  return {
    source,
    folderId,
    crumbs,
    items,
    total,
    loading,
    loadingMore,
    error,
    query,
    hasMore,
    isEmpty,
    selectableCount,
    load,
    enter,
    jumpTo,
    search,
    setSource,
    reset,
    retry: () => load(false, true),
    loadMore: () => load(true),
  }
}
