import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { DriveError } from '@/lib/api/client'
import { driveApi, thumbnailUrl } from '@/lib/api/drive'
import type {
  CloudFile,
  FileOrder,
  FilePermission,
  FilePool,
  KindFilter,
  QuotaSummary,
  UsageSummary,
  ViewMode,
} from '@/lib/api/types'
import { formatCount, formatBytes } from '@/lib/format'
import { translate } from '@/lib/i18n'

import { useNoticeStore } from './notice'

export type BrowsingMode = 'drive' | 'unindexed' | 'recycle'

export interface Crumb {
  id: string | null
  name: string
}

const VIEW_KEY = 'snfs.view'
const MiB = 1024 * 1024

const VIEW_MODES: Record<ViewMode, true> = {
  list: true,
  grid: true,
  column: true,
  waterfall: true,
}

function readView(): ViewMode {
  try {
    const stored = localStorage.getItem(VIEW_KEY) as ViewMode | null
    return stored && VIEW_MODES[stored] ? stored : 'list'
  } catch {
    return 'list'
  }
}

function errorMessage(caught: unknown): string {
  if (caught instanceof DriveError) {
    if (caught.status === 0) return caught.message
    if (caught.status === 403) return translate('notice.forbidden')
    return caught.message
  }
  if (caught instanceof Error) return caught.message
  return translate('notice.noAnswer')
}

function arrangeFoldersFirst(items: CloudFile[]): CloudFile[] {
  const folders = items.filter((item) => item.is_folder)
  const files = items.filter((item) => !item.is_folder)
  return [...folders, ...files]
}

function unique(items: CloudFile[]): CloudFile[] {
  const seen = new Set<string>()
  return items.filter((item) => {
    if (seen.has(item.id)) return false
    seen.add(item.id)
    return true
  })
}

interface CachedPage {
  rows: CloudFile[]
  total: number
  at: number
}

const PAGE_CACHE_LIMIT = 32
/** A cached page is repainted immediately, then revalidated once it is this old. */
const PAGE_REVALIDATE_MS = 8_000

export const useVaultStore = defineStore('vault', () => {
  const notice = useNoticeStore()

  const mode = ref<BrowsingMode>('drive')
  const folderId = ref<string | null>(null)
  const folder = ref<CloudFile | null>(null)
  const crumbs = ref<Crumb[]>([{ id: null, name: translate('nav.drive') }])

  const items = ref<CloudFile[]>([])
  const total = ref(0)
  const loading = ref(false)
  const loadingMore = ref(false)
  const error = ref('')
  const actionBusy = ref<string | null>(null)

  const query = ref('')
  const kind = ref<KindFilter>('all')
  const order = ref<FileOrder>('date')
  const orderDesc = ref(true)
  const poolFilter = ref<string | null>(null)
  const viewMode = ref<ViewMode>(readView())
  // Selection is the checkbox's business alone; activating an object is a
  // separate gesture that opens a tab. Objects are remembered alongside their
  // ids so a selection made in a column can outlive the listing that made it.
  const selection = ref<string[]>([])
  const picked = ref<Record<string, CloudFile>>({})
  /** Toggled by the toolbar; checkboxes render only while it is on. */
  const selectMode = ref(false)
  /** The file whose tab is active: what the inspector and preview describe. */
  const previewFile = ref<CloudFile | null>(null)
  let lastSelected: string | null = null
  let searchTimer: number | undefined
  let loadSeq = 0

  /**
   * Folder listings for the Miller columns, keyed by browsing mode and parent.
   * Columns refetch only after a mutation or an explicit refresh, so walking
   * back up a trail is instant.
   */
  const childrenCache = new Map<string, CloudFile[]>()
  const childrenLoads = new Map<string, Promise<CloudFile[]>>()
  const childrenVersion = ref(0)

  /**
   * First-page listings for the list, grid, and waterfall surfaces, keyed by
   * the whole request (mode, folder, filters, order). Revisiting a folder
   * paints from memory and refreshes in the background; Miller columns keep
   * their own whole-folder cache below.
   */
  const pageCache = new Map<string, CachedPage>()
  const pageLoads = new Map<string, Promise<CachedPage>>()
  let cacheGeneration = 0

  const usage = ref<UsageSummary | null>(null)
  const quota = ref<QuotaSummary | null>(null)
  const pools = ref<FilePool[]>([])
  const poolUsage = ref<Record<string, number>>({})
  const volumeLoading = ref(false)
  const volumeError = ref('')

  const poolRows = computed(() =>
    pools.value.map((pool) => ({
      pool,
      mb: poolUsage.value[pool.id] ?? 0,
      bytes: (poolUsage.value[pool.id] ?? 0) * MiB,
    })),
  )

  const usedBytes = computed(() => {
    if (!usage.value) return 0
    return usage.value.total_usage_bytes > 0
      ? usage.value.total_usage_bytes
      : usage.value.used_quota * MiB
  })
  const quotaBytes = computed(() => {
    const totalMb = quota.value?.total_quota ?? usage.value?.total_quota ?? 0
    return totalMb * MiB
  })
  const extraBytes = computed(() => (quota.value?.extra_quota ?? 0) * MiB)
  const baseBytes = computed(() => Math.max(0, quotaBytes.value - extraBytes.value))
  const remainingBytes = computed(() => Math.max(0, quotaBytes.value - usedBytes.value))
  const usedFraction = computed(() =>
    quotaBytes.value > 0 ? Math.min(1, usedBytes.value / quotaBytes.value) : 0,
  )
  const fileCount = computed(() => usage.value?.total_file_count ?? 0)

  const selectedItems = computed(() =>
    selection.value
      .map((id) => picked.value[id])
      .filter((item): item is CloudFile => item !== undefined),
  )

  const selectedBytes = computed(() =>
    selectedItems.value.reduce((sum, item) => sum + (item.object?.size ?? 0), 0),
  )
  const activePool = computed(
    () => pools.value.find((pool) => pool.id === poolFilter.value) ?? null,
  )
  const canGoUp = computed(() => (folder.value ? (folder.value.parent_id ?? null) : null))
  const isEmpty = computed(() => !loading.value && !error.value && items.value.length === 0)
  const hasMore = computed(() => items.value.length < total.value)
  const showingLabel = computed(() => {
    if (loading.value) return translate('status.reading')
    if (total.value === 0) return translate('status.noObjects')
    if (items.value.length < total.value) {
      return translate('status.objectsOf', {
        shown: formatCount(items.value.length),
        total: formatCount(total.value),
      })
    }
    return translate(total.value === 1 ? 'status.oneObject' : 'status.objectsCount', {
      n: formatCount(total.value),
    })
  })

  function storeView(next: ViewMode) {
    viewMode.value = next
    try {
      localStorage.setItem(VIEW_KEY, next)
    } catch {
      // Preference persistence is optional.
    }
  }

  const VIEW_CYCLE: ViewMode[] = ['list', 'grid', 'column', 'waterfall']

  function cycleView() {
    const available =
      mode.value === 'unindexed'
        ? VIEW_CYCLE.filter((entry) => entry !== 'column')
        : VIEW_CYCLE
    const index = available.indexOf(viewMode.value)
    storeView(available[(index + 1) % available.length] ?? 'list')
  }

  function clearSelection() {
    selection.value = []
    picked.value = {}
    lastSelected = null
  }

  /** Turns the dedicated selection mode on or off; leaving it clears the ticks. */
  function setSelectMode(next: boolean) {
    selectMode.value = next
    if (!next) clearSelection()
  }

  function toggleSelectMode() {
    setSelectMode(!selectMode.value)
  }

  function remember(files: CloudFile[]) {
    const next = { ...picked.value }
    for (const file of files) next[file.id] = file
    picked.value = next
  }

  /** Drops remembered rows that are no longer ticked. */
  function prunePicked() {
    const next: Record<string, CloudFile> = {}
    for (const id of selection.value) {
      const file = picked.value[id]
      if (file) next[id] = file
    }
    picked.value = next
  }

  /** Ticks or unticks one checkbox. Shift extends the range from the anchor. */
  function toggle(file: CloudFile, range = false) {
    if (range && lastSelected) {
      const ids = items.value.map((entry) => entry.id)
      const from = ids.indexOf(lastSelected)
      const to = ids.indexOf(file.id)
      if (from >= 0 && to >= 0) {
        const [start, end] = from < to ? [from, to] : [to, from]
        selection.value = ids.slice(start, end + 1)
        remember(items.value)
        prunePicked()
        return
      }
    }

    const next = new Set(selection.value)
    if (next.has(file.id)) {
      next.delete(file.id)
    } else {
      next.add(file.id)
      remember([file])
    }
    selection.value = [...next]
    prunePicked()
    lastSelected = file.id
  }

  function selectAll() {
    selection.value = items.value.map((item) => item.id)
    remember(items.value)
    prunePicked()
    lastSelected = selection.value[selection.value.length - 1] ?? null
  }

  /** Records the file a tab is showing, for the preview body and inspector. */
  function setPreviewFile(file: CloudFile | null) {
    previewFile.value = file
  }

  function patchItem(id: string, changes: Partial<CloudFile>) {
    items.value = items.value.map((item) => (item.id === id ? { ...item, ...changes } : item))
    if (picked.value[id]) picked.value = { ...picked.value, [id]: { ...picked.value[id], ...changes } }
    if (previewFile.value?.id === id) previewFile.value = { ...previewFile.value, ...changes }
  }

  function buildOptions(offset: number) {
    const options = {
      offset,
      take: 50,
      query: query.value,
      order: order.value,
      orderDesc: orderDesc.value,
      pool_id: poolFilter.value ?? undefined,
      is_folder: kind.value === 'folders' ? true : kind.value === 'files' ? false : undefined,
    }
    if (mode.value === 'recycle') return { ...options, recycled: true }
    if (mode.value === 'unindexed') return { ...options, recycled: false }
    return { ...options, recycled: false }
  }

  async function fetchPage(offset: number, signal?: AbortSignal) {
    const options = buildOptions(offset)
    if (mode.value === 'recycle') return driveApi.owned(options, signal)
    if (mode.value === 'unindexed') return driveApi.unindexed(options, signal)
    return folderId.value
      ? driveApi.children(folderId.value, options, signal)
      : driveApi.root(options, signal)
  }

  /** The cache identity of the current listing; searches are never cached. */
  function listingCacheKey(): string | null {
    if (query.value.trim()) return null
    const folder = mode.value === 'drive' ? (folderId.value ?? '') : ''
    return [
      mode.value,
      folder,
      poolFilter.value ?? '',
      kind.value,
      order.value,
      orderDesc.value ? 'desc' : 'asc',
    ].join('|')
  }

  /** Reads a page and remembers it, unless a mutation retired this generation. */
  function requestPage(key: string): Promise<CachedPage> {
    const pending = pageLoads.get(key)
    if (pending) return pending
    const generation = cacheGeneration
    const request = fetchPage(0).then((page) => {
      const cached = { rows: arrangeFoldersFirst(page.items), total: page.total, at: Date.now() }
      if (generation === cacheGeneration) rememberPage(key, cached)
      return cached
    })
    pageLoads.set(key, request)
    request.then(
      () => pageLoads.delete(key),
      () => pageLoads.delete(key),
    )
    return request
  }

  function rememberPage(key: string, page: CachedPage) {
    pageCache.delete(key)
    pageCache.set(key, page)
    while (pageCache.size > PAGE_CACHE_LIMIT) {
      const oldest = pageCache.keys().next().value
      if (oldest === undefined) break
      pageCache.delete(oldest)
    }
  }

  function cachedPage(key: string): CachedPage | undefined {
    const hit = pageCache.get(key)
    if (!hit) return undefined
    // Touch on read so the least recently visited folder leaves first.
    pageCache.delete(key)
    pageCache.set(key, hit)
    return hit
  }

  /**
   * A cached page is already on screen; ask once more after the paint so a
   * change made in another tab catches up without blocking the first paint.
   */
  async function revalidatePage(key: string, seq: number, baseLength: number) {
    try {
      const page = await requestPage(key)
      if (seq !== loadSeq || listingCacheKey() !== key) return
      // The person asked for more rows while this was in flight; keep them.
      if (items.value.length > baseLength) return
      items.value = page.rows
      total.value = page.total
    } catch {
      // The painted page stays; a failed background refresh is not an error.
    }
  }

  function applyPage(page: CachedPage) {
    items.value = page.rows
    total.value = page.total
    clearSelection()
  }

  async function load() {
    const seq = ++loadSeq
    error.value = ''
    // Columns already list the open folder; sharing their request means a
    // mount loads one listing instead of racing a second copy of it.
    if (viewMode.value === 'column' && mode.value !== 'unindexed') {
      loading.value = true
      try {
        const parent = mode.value === 'drive' ? folderId.value : null
        const rows = await listChildren(parent)
        if (seq !== loadSeq) return
        items.value = rows
        total.value = rows.length
        clearSelection()
      } catch (caught) {
        if (seq !== loadSeq) return
        error.value = errorMessage(caught)
        items.value = []
        total.value = 0
      } finally {
        if (seq === loadSeq) loading.value = false
      }
      return
    }

    const key = listingCacheKey()
    const cached = key ? cachedPage(key) : undefined
    if (cached) {
      items.value = cached.rows
      total.value = cached.total
      loading.value = false
      clearSelection()
      if (Date.now() - cached.at >= PAGE_REVALIDATE_MS) {
        void revalidatePage(key!, seq, cached.rows.length)
      }
      return
    }

    loading.value = true
    try {
      const page = key
        ? await requestPage(key)
        : await fetchPage(0).then((listing) => ({
            rows: arrangeFoldersFirst(listing.items),
            total: listing.total,
            at: Date.now(),
          }))
      if (seq !== loadSeq) return
      applyPage(page)
    } catch (caught) {
      if (seq !== loadSeq) return
      if (caught instanceof DOMException && caught.name === 'AbortError') return
      error.value = errorMessage(caught)
      items.value = []
      total.value = 0
    } finally {
      if (seq === loadSeq) loading.value = false
    }
  }

  async function loadMore() {
    if (loading.value || loadingMore.value || !hasMore.value) return
    loadingMore.value = true
    try {
      const page = await fetchPage(items.value.length)
      const merged = unique([...items.value, ...page.items])
      items.value = arrangeFoldersFirst(merged)
      total.value = page.total
    } catch (caught) {
      notice.error(errorMessage(caught))
    } finally {
      loadingMore.value = false
    }
  }

  async function open(nextMode: BrowsingMode, nextFolderId: string | null = null) {
    mode.value = nextMode
    folderId.value = nextFolderId
    query.value = ''
    kind.value = 'all'
    if (nextMode !== 'drive') poolFilter.value = null
    clearSelection()

    if (nextMode !== 'drive') {
      folder.value = null
      crumbs.value = [
        {
          id: null,
          name: translate(nextMode === 'unindexed' ? 'nav.unindexed' : 'nav.recycle'),
        },
      ]
      await load()
      return
    }

    if (!nextFolderId) {
      folder.value = null
      crumbs.value = [{ id: null, name: translate('nav.drive') }]
      await load()
      return
    }

    loading.value = true
    error.value = ''
    try {
      const [info, chain] = await Promise.all([
        driveApi.file(nextFolderId),
        driveApi.breadcrumb(nextFolderId),
      ])
      folder.value = info
      crumbs.value = [
        { id: null, name: translate('nav.drive') },
        ...chain.map((crumb) => ({ id: crumb.id, name: crumb.name })),
      ]
    } catch (caught) {
      error.value = errorMessage(caught)
      folder.value = null
      crumbs.value = [{ id: null, name: translate('nav.drive') }]
    } finally {
      loading.value = false
    }
    await load()
  }

  async function loadVolume() {
    volumeLoading.value = true
    volumeError.value = ''
    try {
      const [usageResult, quotaResult, poolsResult] = await Promise.allSettled([
        driveApi.usage(),
        driveApi.quota(),
        driveApi.pools(),
      ])

      if (usageResult.status === 'fulfilled') usage.value = usageResult.value
      if (quotaResult.status === 'fulfilled') quota.value = quotaResult.value
      if (poolsResult.status === 'fulfilled') {
        pools.value = poolsResult.value.items.filter((pool) => !pool.is_hidden)
      }

      const entries = await Promise.allSettled(
        pools.value.map(async (pool) => {
          const usageResult = await driveApi.poolUsage(pool.id)
          return [pool.id, usageResult.total_quota] as const
        }),
      )
      const next: Record<string, number> = {}
      for (const entry of entries) {
        if (entry.status === 'fulfilled') {
          const [id, mb] = entry.value
          next[id] = mb
        }
      }
      poolUsage.value = next

      if (
        usageResult.status === 'rejected' &&
        quotaResult.status === 'rejected' &&
        poolsResult.status === 'rejected'
      ) {
        volumeError.value = errorMessage(usageResult.reason)
      }
    } catch (caught) {
      volumeError.value = errorMessage(caught)
    } finally {
      volumeLoading.value = false
    }
  }

  async function refreshAll() {
    invalidateChildren()
    await Promise.all([load(), loadVolume()])
  }

  function setQuery(value: string) {
    query.value = value
    if (searchTimer) window.clearTimeout(searchTimer)
    if (!value.trim()) {
      void load()
      return
    }
    searchTimer = window.setTimeout(() => {
      void load()
    }, 240)
  }

  function setKind(next: KindFilter) {
    if (kind.value === next) return
    kind.value = next
    void load()
  }

  function setOrder(next: FileOrder) {
    if (order.value === next) {
      orderDesc.value = !orderDesc.value
    } else {
      order.value = next
      orderDesc.value = next === 'name' ? false : true
    }
    void load()
  }

  function setPoolFilter(poolId: string | null) {
    poolFilter.value = poolId
    void load()
  }

  function setFolderName(name: string) {
    if (!folder.value) return
    folder.value = { ...folder.value, name }
    const last = crumbs.value[crumbs.value.length - 1]
    if (last) last.name = name
  }

  async function rename(file: CloudFile, name: string) {
    const trimmed = name.trim()
    if (!trimmed || trimmed === file.name) return
    actionBusy.value = 'rename'
    try {
      await driveApi.rename(file.id, trimmed)
      invalidateChildren()
      if (folder.value?.id === file.id) setFolderName(trimmed)
      notice.ok(translate('notice.renamed', { name: trimmed }))
      await load()
    } catch (caught) {
      notice.error(errorMessage(caught))
      throw caught
    } finally {
      actionBusy.value = null
    }
  }

  async function createFolder(name: string) {
    const trimmed = name.trim()
    if (!trimmed) return
    actionBusy.value = 'new-folder'
    try {
      await driveApi.createFolder(trimmed, folderId.value)
      invalidateChildren()
      notice.ok(translate('notice.folderCreated', { name: trimmed }))
      await Promise.all([load(), loadVolume()])
    } catch (caught) {
      notice.error(errorMessage(caught))
      throw caught
    } finally {
      actionBusy.value = null
    }
  }

  async function move(files: CloudFile[], parentId: string | null) {
    if (files.length === 0) return
    const ids = files.map((file) => file.id)
    actionBusy.value = 'move'
    try {
      await driveApi.move(ids, parentId)
      invalidateChildren()
      notice.ok(
        files.length === 1
          ? translate('notice.oneMoved', { name: files[0]?.name ?? translate('dialog.object') })
          : translate('notice.manyMoved', { n: formatCount(files.length) }),
      )
      clearSelection()
      await load()
    } catch (caught) {
      notice.error(errorMessage(caught))
      throw caught
    } finally {
      actionBusy.value = null
    }
  }

  async function recycle(files: CloudFile[]) {
    if (files.length === 0) return
    const ids = files.map((file) => file.id)
    actionBusy.value = 'recycle'
    try {
      await driveApi.recycle(ids)
      invalidateChildren()
      notice.ok(
        files.length === 1
          ? translate('notice.oneRecycled', {
              name: files[0]?.name ?? translate('dialog.object'),
            })
          : translate('notice.manyRecycled', { n: formatCount(files.length) }),
      )
      clearSelection()
      await Promise.all([load(), loadVolume()])
    } catch (caught) {
      notice.error(errorMessage(caught))
      throw caught
    } finally {
      actionBusy.value = null
    }
  }

  async function restore(files: CloudFile[]) {
    if (files.length === 0) return
    const ids = files.map((file) => file.id)
    actionBusy.value = 'restore'
    try {
      await driveApi.restore(ids)
      invalidateChildren()
      notice.ok(
        files.length === 1
          ? translate('notice.oneRestored', {
              name: files[0]?.name ?? translate('dialog.object'),
            })
          : translate('notice.manyRestored', { n: formatCount(files.length) }),
      )
      clearSelection()
      await Promise.all([load(), loadVolume()])
    } catch (caught) {
      notice.error(errorMessage(caught))
      throw caught
    } finally {
      actionBusy.value = null
    }
  }

  async function purge(files: CloudFile[]) {
    if (files.length === 0) return
    const ids = files.map((file) => file.id)
    actionBusy.value = 'purge'
    try {
      await driveApi.purge(ids)
      invalidateChildren()
      notice.ok(
        files.length === 1
          ? translate('notice.onePurged', {
              name: files[0]?.name ?? translate('dialog.object'),
            })
          : translate('notice.manyPurged', { n: formatCount(files.length) }),
      )
      clearSelection()
      await Promise.all([load(), loadVolume()])
    } catch (caught) {
      notice.error(errorMessage(caught))
      throw caught
    } finally {
      actionBusy.value = null
    }
  }

  async function permissionsFor(file: CloudFile): Promise<FilePermission[]> {
    return driveApi.permissions(file.id)
  }

  async function setVisibility(file: CloudFile, isPublic: boolean) {
    actionBusy.value = 'visibility'
    try {
      const current = await permissionsFor(file)
      const next = current.filter(
        (permission) =>
          permission.subject_type !== 'public' && permission.subject_type !== 'private',
      )
      next.push({
        id: undefined,
        file_id: file.id,
        subject_type: isPublic ? 'public' : 'private',
        subject_id: '',
        permission: 'read',
      })
      await driveApi.setPermissions(file.id, next)
      invalidateChildren()
      notice.ok(translate(isPublic ? 'notice.published' : 'notice.unpublished'))
      await load()
    } catch (caught) {
      notice.error(errorMessage(caught))
      throw caught
    } finally {
      actionBusy.value = null
    }
  }

  /** Replaces the file's access rules with the edited set. */
  async function savePermissions(file: CloudFile, items: FilePermission[]) {
    actionBusy.value = 'permissions'
    try {
      await driveApi.setPermissions(file.id, items)
      invalidateChildren()
      notice.ok(translate('notice.permissionsUpdated'))
      await load()
    } catch (caught) {
      notice.error(errorMessage(caught))
      throw caught
    } finally {
      actionBusy.value = null
    }
  }

  async function listFolders(parentId: string | null): Promise<CloudFile[]> {
    const options = {
      is_folder: true,
      take: 200,
      order: 'name' as const,
      orderDesc: false,
      recycled: false,
    }
    const page = parentId
      ? await driveApi.children(parentId, options)
      : await driveApi.root(options)
    return page.items
  }

  /** Drops every cached column listing and asks open columns to reload. */
  function invalidateChildren() {
    childrenCache.clear()
    pageCache.clear()
    cacheGeneration += 1
    childrenVersion.value += 1
  }

  /** Every entry under a folder, folders and files together, for columns. */
  async function listChildren(parentId: string | null): Promise<CloudFile[]> {
    const key = `${mode.value}:${parentId ?? ''}`
    const cached = childrenCache.get(key)
    if (cached) return cached
    const pending = childrenLoads.get(key)
    if (pending) return pending

    const request = (async () => {
      const options = {
        take: 200,
        order: 'name' as const,
        orderDesc: false,
        recycled: false,
      }
      let items: CloudFile[]
      if (parentId) {
        items = (await driveApi.children(parentId, options)).items
      } else if (mode.value === 'recycle') {
        items = (await driveApi.owned({ ...options, recycled: true })).items
      } else if (mode.value === 'unindexed') {
        items = (await driveApi.unindexed(options)).items
      } else {
        items = (await driveApi.root(options)).items
      }
      const rows = arrangeFoldersFirst(items)
      childrenCache.set(key, rows)
      return rows
    })()

    childrenLoads.set(key, request)
    try {
      return await request
    } finally {
      childrenLoads.delete(key)
    }
  }

  function previewFor(file: CloudFile): string {
    return thumbnailUrl(file.id)
  }

  function errorSummary(): string {
    if (volumeError.value) return volumeError.value
    return `${formatBytes(usedBytes.value)} of ${formatBytes(quotaBytes.value)} used`
  }

  return {
    mode,
    folderId,
    folder,
    crumbs,
    items,
    total,
    loading,
    loadingMore,
    error,
    actionBusy,
    query,
    kind,
    order,
    orderDesc,
    poolFilter,
    viewMode,
    selection,
    previewFile,
    usage,
    quota,
    pools,
    poolUsage,
    poolRows,
    volumeLoading,
    volumeError,
    usedBytes,
    quotaBytes,
    extraBytes,
    baseBytes,
    remainingBytes,
    usedFraction,
    fileCount,
    selectedItems,
    selectMode,
    setSelectMode,
    toggleSelectMode,
    childrenVersion,
    invalidateChildren,
    selectedBytes,
    activePool,
    canGoUp,
    isEmpty,
    hasMore,
    showingLabel,
    open,
    load,
    loadMore,
    refreshAll,
    loadVolume,
    setQuery,
    setKind,
    setOrder,
    setPoolFilter,
    toggle,
    selectAll,
    clearSelection,
    setPreviewFile,
    patchItem,
    cycleView,
    storeView,
    rename,
    createFolder,
    move,
    recycle,
    restore,
    purge,
    permissionsFor,
    savePermissions,
    setVisibility,
    listFolders,
    listChildren,
    previewFor,
    errorSummary,
    errorMessage,
  }
})
