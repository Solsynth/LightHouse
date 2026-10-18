<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import {
  Checkmark16,
  Close16,
  Edit16,
  Favorite16,
  FavoriteFilled16,
  Locked16,
  Notification16,
  NotificationOff16,
  Renew16,
  Unlocked16,
  UserFollow16,
  UserMinus16,
} from '@carbon/icons-vue'

import { useNoticeStore } from '@/stores/notice'
import { useSessionStore } from '@/stores/session'
import { accountApi, relationshipApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { Account, FriendOverview, Relationship } from '@/lib/api/types'
import { RELATIONSHIP_STATUS } from '@/lib/enums'
import { relative, remaining, utcStamp } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import ConfirmAction from '@/components/ConfirmAction.vue'
import IconButton from '@/components/IconButton.vue'
import TabBox from '@/components/TabBox.vue'
import type { Tab } from '@/components/tab'

const { t } = useI18n()
const notice = useNoticeStore()
const session = useSessionStore()

type Panel = 'friends' | 'requests' | 'close' | 'blocked' | 'muted'
type PendingAction = { kind: 'remove' | 'block' | 'decline'; row: Relationship }

const tabs = computed<Tab[]>(() => [
  { key: 'friends', label: t('relationships.tabs.friends') },
  { key: 'requests', label: t('relationships.tabs.requests') },
  { key: 'close', label: t('relationships.tabs.close') },
  { key: 'blocked', label: t('relationships.tabs.blocked') },
  { key: 'muted', label: t('relationships.tabs.muted') },
])

const panel = ref<Panel>('friends')

const rows = ref<Relationship[]>([])
const pending = ref<Relationship[]>([])
const presence = ref<Map<string, FriendOverview['status']>>(new Map())
const total = ref(0)
const loading = ref(true)
const loadingMore = ref(false)
const error = ref('')
const busy = ref(false)

const query = ref('')
const results = ref<Account[]>([])
const searching = ref(false)
const searched = ref(false)
const searchError = ref('')

const editingId = ref('')
const aliasDraft = ref('')

const pendingAction = ref<PendingAction | null>(null)

const meId = computed(() => session.account?.id ?? '')
const pageSize = 200

/** The account on the far side of a row, from this account's point of view. */
function counterpart(row: Relationship): Account | undefined {
  return row.account_id === meId.value ? row.related : row.account
}

function displayName(account?: Account): string {
  if (!account) return t('relationships.unknownAccount')
  const parts = [
    account.profile?.first_name,
    account.profile?.middle_name,
    account.profile?.last_name,
  ]
    .filter(Boolean)
    .join(' ')
  return account.nick || parts || account.name
}

/** The name this account reads in its own lists: the alias wins. */
function title(row: Relationship): string {
  return row.alias || displayName(counterpart(row))
}

const friends = computed(() =>
  rows.value
    .filter(
      (row) =>
        row.status === RELATIONSHIP_STATUS.friends ||
        row.status === RELATIONSHIP_STATUS.closeFriend,
    )
    .sort((a, b) => {
      const aOnline = presence.value.get(counterpart(a)?.id ?? '')?.is_online ? 0 : 1
      const bOnline = presence.value.get(counterpart(b)?.id ?? '')?.is_online ? 0 : 1
      if (aOnline !== bOnline) return aOnline - bOnline
      return title(a).localeCompare(title(b))
    }),
)

const closeFriends = computed(() =>
  rows.value.filter((row) => row.status === RELATIONSHIP_STATUS.closeFriend),
)
const blocked = computed(() =>
  rows.value.filter((row) => row.status === RELATIONSHIP_STATUS.blocked),
)
const muted = computed(() => rows.value.filter((row) => row.status === RELATIONSHIP_STATUS.muted))

/** Requests the other side sent; the ones this account sent live in the sidebar. */
const incoming = computed(() => pending.value.filter((row) => row.related_id === meId.value))
const outgoing = computed(() => pending.value.filter((row) => row.account_id === meId.value))

const current = computed<Relationship[]>(() => {
  switch (panel.value) {
    case 'requests':
      return incoming.value
    case 'close':
      return closeFriends.value
    case 'blocked':
      return blocked.value
    case 'muted':
      return muted.value
    default:
      return friends.value
  }
})

const panelTitle = computed(() => {
  switch (panel.value) {
    case 'requests':
      return t('relationships.incomingRequests')
    case 'close':
      return t('relationships.closeFriends')
    case 'blocked':
      return t('relationships.blockedTitle')
    case 'muted':
      return t('relationships.mutedTitle')
    default:
      return t('relationships.friendList')
  }
})

const emptyCopy = computed(() => {
  switch (panel.value) {
    case 'requests':
      return t('relationships.emptyRequests')
    case 'close':
      return t('relationships.emptyClose')
    case 'blocked':
      return t('relationships.emptyBlocked')
    case 'muted':
      return t('relationships.emptyMuted')
    default:
      return t('relationships.emptyFriends')
  }
})

/** The viewer's standing with a search result, or empty when there is none. */
function standing(account: Account): string {
  const outgoingRow = rows.value.find((row) => row.related_id === account.id)
  if (outgoingRow) {
    switch (outgoingRow.status) {
      case RELATIONSHIP_STATUS.closeFriend:
        return t('relationships.standing.closeFriend')
      case RELATIONSHIP_STATUS.friends:
        return t('relationships.standing.friends')
      case RELATIONSHIP_STATUS.blocked:
        return t('relationships.standing.blocked')
      default:
        return t('relationships.standing.muted')
    }
  }
  if (pending.value.some((row) => row.account_id === meId.value && row.related_id === account.id)) {
    return t('relationships.standing.requested')
  }
  if (pending.value.some((row) => row.account_id === account.id && row.related_id === meId.value)) {
    return t('relationships.standing.incoming')
  }
  return ''
}

/**
 * The server labels presence in English ("Online"), so the console reads the
 * booleans and translates them for a non-English catalog instead of printing
 * the server's word.
 */
function presenceLabel(row: Relationship): string {
  const status = presenceOf(row)
  if (!status?.is_online) return t('common.offline')
  return status.is_idle ? t('relationships.idle') : t('common.online')
}

function presenceOf(row: Relationship): FriendOverview['status'] | undefined {
  return presence.value.get(counterpart(row)?.id ?? '')
}

function selectPanel(key: string) {
  panel.value = key as Panel
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [page, requests, overview] = await Promise.all([
      relationshipApi.list({ take: pageSize, offset: 0 }),
      relationshipApi.requests(),
      relationshipApi.overview(),
    ])
    rows.value = page.items
    total.value = page.total
    pending.value = requests ?? []
    presence.value = new Map(overview.map((item) => [item.account.id, item.status]))
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('relationships.errorLoad')
  } finally {
    loading.value = false
  }
}

/**
 * Appends the next page. The endpoint pages over every non-pending row, so the
 * tabs filter what has been loaded instead of asking for a per-tab page.
 */
async function loadMore() {
  if (loadingMore.value || rows.value.length >= total.value) return
  loadingMore.value = true
  try {
    const page = await relationshipApi.list({ take: pageSize, offset: rows.value.length })
    rows.value = [...rows.value, ...page.items]
    total.value = page.total
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('relationships.errorLoad'))
  } finally {
    loadingMore.value = false
  }
}

async function run(action: () => Promise<unknown>, ok: string) {
  busy.value = true
  try {
    await action()
    notice.ok(ok)
    await load()
  } catch (caught) {
    notice.fail(caught instanceof Error ? caught.message : t('relationships.errorAction'))
  } finally {
    busy.value = false
  }
}

function startAlias(row: Relationship) {
  editingId.value = counterpart(row)?.id ?? ''
  aliasDraft.value = row.alias ?? ''
}

async function saveAlias(row: Relationship) {
  const accountId = counterpart(row)?.id
  if (!accountId) return
  const alias = aliasDraft.value.trim()
  editingId.value = ''
  await run(
    () => relationshipApi.updateAlias(accountId, alias || null),
    alias ? t('relationships.noticeAlias') : t('relationships.noticeAliasCleared'),
  )
}

/** Close-friend status is a toggle: the row says which way it will go. */
function isCloseFriend(row: Relationship): boolean {
  return row.status === RELATIONSHIP_STATUS.closeFriend
}

function toggleCloseFriend(row: Relationship) {
  const accountId = counterpart(row)?.id
  if (!accountId) return
  if (isCloseFriend(row)) {
    void run(
      () => relationshipApi.removeCloseFriend(accountId),
      t('relationships.noticeCloseRemoved'),
    )
  } else {
    void run(() => relationshipApi.addCloseFriend(accountId), t('relationships.noticeCloseAdded'))
  }
}

function muteRow(row: Relationship) {
  const accountId = counterpart(row)?.id
  if (!accountId) return
  void run(
    () => relationshipApi.mute(accountId),
    t('relationships.noticeMuted', { account: title(row) }),
  )
}

function unblockRow(row: Relationship) {
  const accountId = counterpart(row)?.id
  if (!accountId) return
  void run(
    () => relationshipApi.unblock(accountId),
    t('relationships.noticeUnblocked', { account: title(row) }),
  )
}

function unmuteRow(row: Relationship) {
  const accountId = counterpart(row)?.id
  if (!accountId) return
  void run(
    () => relationshipApi.unmute(accountId),
    t('relationships.noticeUnmuted', { account: title(row) }),
  )
}

function acceptRow(row: Relationship) {
  const accountId = counterpart(row)?.id
  if (!accountId) return
  void run(
    () => relationshipApi.acceptRequest(accountId),
    t('relationships.noticeAccepted', { account: title(row) }),
  )
}

function cancelRow(row: Relationship) {
  const accountId = counterpart(row)?.id
  if (!accountId) return
  void run(() => relationshipApi.cancelRequest(accountId), t('relationships.noticeCancelled'))
}

function requestFrom(account: Account) {
  void run(
    () => relationshipApi.sendRequest(account.id),
    t('relationships.noticeRequested', { account: displayName(account) }),
  )
}

/** Removal, blocking, and declining are destructive, so they confirm first. */
function confirmAction() {
  const target = pendingAction.value
  if (!target) return
  const accountId = counterpart(target.row)?.id
  if (!accountId) {
    pendingAction.value = null
    return
  }
  const account = title(target.row)
  const actions: Record<PendingAction['kind'], () => Promise<unknown>> = {
    remove: () => relationshipApi.removeFriend(accountId),
    block: () => relationshipApi.block(accountId),
    decline: () => relationshipApi.declineRequest(accountId),
  }
  const notices: Record<PendingAction['kind'], string> = {
    remove: t('relationships.noticeRemoved', { account }),
    block: t('relationships.noticeBlocked', { account }),
    decline: t('relationships.noticeDeclined', { account }),
  }
  void run(actions[target.kind], notices[target.kind])
  pendingAction.value = null
}

const confirmCopy = computed(() => {
  const target = pendingAction.value
  if (!target) return { title: '', body: '', action: '' }
  const account = title(target.row)
  if (target.kind === 'remove') {
    return {
      title: t('relationships.confirmRemoveTitle'),
      body: t('relationships.confirmRemoveBody', { account }),
      action: t('relationships.removeFriend'),
    }
  }
  if (target.kind === 'block') {
    return {
      title: t('relationships.confirmBlockTitle'),
      body: t('relationships.confirmBlockBody', { account }),
      action: t('relationships.block'),
    }
  }
  return {
    title: t('relationships.confirmDeclineTitle'),
    body: t('relationships.confirmDeclineBody', { account }),
    action: t('relationships.decline'),
  }
})

async function search() {
  const term = query.value.trim()
  searched.value = false
  searchError.value = ''
  if (!term) {
    results.value = []
    return
  }
  searching.value = true
  try {
    const found = await accountApi.search(term, 10)
    results.value = found.filter((account) => account.id !== meId.value)
    searched.value = true
  } catch (caught) {
    searchError.value =
      caught instanceof StargateError ? caught.message : t('relationships.errorSearch')
  } finally {
    searching.value = false
  }
}

function clearSearch() {
  query.value = ''
  results.value = []
  searched.value = false
  searchError.value = ''
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader :title="t('relationships.title')" :description="t('relationships.description')">
      <template #actions>
        <IconButton :label="t('common.refresh')" :disabled="loading" @click="load">
          <Renew16 aria-hidden="true" />
        </IconButton>
      </template>
    </PageHeader>

    <TabBox
      :tabs="tabs"
      :active="panel"
      :label="t('relationships.tabsLabel')"
      @select="selectPanel"
    />

    <p v-if="error" class="mb-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

    <div class="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
      <ConsolePanel
        :title="panelTitle"
        :note="loading ? undefined : t('relationships.totalNote', { n: current.length })"
      >
        <LoadingSkeleton v-if="loading" variant="rows" :rows="4" />

        <p v-else-if="!current.length" class="voice py-6">{{ emptyCopy }}</p>

        <ul v-else>
          <li
            v-for="row in current"
            :key="row.related_id"
            class="flex flex-wrap items-start justify-between gap-3 py-3 first:pt-0 last:pb-0"
          >
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <template v-if="editingId && editingId === counterpart(row)?.id">
                  <input
                    v-model="aliasDraft"
                    class="input input-xs max-w-[180px]"
                    :aria-label="t('relationships.alias')"
                    :placeholder="displayName(counterpart(row))"
                    @keyup.enter="saveAlias(row)"
                    @keyup.esc="editingId = ''"
                  />
                  <IconButton :label="t('common.save')" :disabled="busy" @click="saveAlias(row)">
                    <Checkmark16 aria-hidden="true" />
                  </IconButton>
                  <IconButton :label="t('common.cancel')" @click="editingId = ''">
                    <Close16 aria-hidden="true" />
                  </IconButton>
                </template>
                <template v-else>
                  <RouterLink
                    v-if="counterpart(row)"
                    :to="`/@${encodeURIComponent(counterpart(row)!.name)}`"
                    class="text-xs text-base-content hover:underline"
                  >
                    {{ title(row) }}
                  </RouterLink>
                  <span v-else class="text-xs text-base-content">{{ title(row) }}</span>
                  <span v-if="row.alias && counterpart(row)" class="text-2xs text-faint">
                    {{ displayName(counterpart(row)) }}
                  </span>
                </template>

                <span v-if="panel === 'friends' && presenceOf(row)">
                  <span
                    class="chip"
                    :class="presenceOf(row)?.is_online ? 'text-success' : 'text-faint'"
                  >
                    <span
                      class="dot"
                      :class="presenceOf(row)?.is_online ? 'bg-success' : 'bg-line'"
                    />
                    {{ presenceLabel(row) }}
                  </span>
                </span>
                <span v-if="isCloseFriend(row)" class="chip text-accent">
                  {{ t('relationships.standing.closeFriend') }}
                </span>
                <span v-if="panel === 'requests'" class="chip text-warning">
                  {{ t('relationships.incoming') }}
                </span>
                <span
                  v-if="row.expired_at && (panel === 'blocked' || panel === 'muted')"
                  class="chip"
                  :title="utcStamp(row.expired_at)"
                >
                  {{ t('relationships.lapses', { time: remaining(row.expired_at) }) }}
                </span>
              </div>
              <p class="mt-1 text-2xs text-faint">
                <span v-if="counterpart(row)">@{{ counterpart(row)!.name }}</span>
                <template v-if="row.created_at">
                  · {{ t('relationships.since', { time: relative(row.created_at) }) }}
                </template>
              </p>
            </div>

            <div v-if="counterpart(row)" class="flex items-center gap-1">
              <template v-if="panel === 'friends'">
                <IconButton
                  :label="t('relationships.editAlias')"
                  :disabled="busy"
                  @click="startAlias(row)"
                >
                  <Edit16 aria-hidden="true" />
                </IconButton>
                <IconButton
                  :label="
                    isCloseFriend(row)
                      ? t('relationships.removeCloseFriend')
                      : t('relationships.addCloseFriend')
                  "
                  :disabled="busy"
                  @click="toggleCloseFriend(row)"
                >
                  <FavoriteFilled16 v-if="isCloseFriend(row)" aria-hidden="true" />
                  <Favorite16 v-else aria-hidden="true" />
                </IconButton>
                <IconButton :label="t('relationships.mute')" :disabled="busy" @click="muteRow(row)">
                  <NotificationOff16 aria-hidden="true" />
                </IconButton>
                <IconButton
                  :label="t('relationships.block')"
                  tone="danger"
                  :disabled="busy"
                  @click="pendingAction = { kind: 'block', row }"
                >
                  <Locked16 aria-hidden="true" />
                </IconButton>
                <IconButton
                  :label="t('relationships.removeFriend')"
                  tone="danger"
                  :disabled="busy"
                  @click="pendingAction = { kind: 'remove', row }"
                >
                  <UserMinus16 aria-hidden="true" />
                </IconButton>
              </template>

              <template v-else-if="panel === 'requests'">
                <IconButton
                  :label="t('relationships.accept')"
                  :disabled="busy"
                  @click="acceptRow(row)"
                >
                  <Checkmark16 aria-hidden="true" />
                </IconButton>
                <IconButton
                  :label="t('relationships.decline')"
                  tone="danger"
                  :disabled="busy"
                  @click="pendingAction = { kind: 'decline', row }"
                >
                  <Close16 aria-hidden="true" />
                </IconButton>
              </template>

              <IconButton
                v-else-if="panel === 'close'"
                :label="t('relationships.removeCloseFriend')"
                :disabled="busy"
                @click="toggleCloseFriend(row)"
              >
                <FavoriteFilled16 aria-hidden="true" />
              </IconButton>

              <IconButton
                v-else-if="panel === 'blocked'"
                :label="t('relationships.unblock')"
                :disabled="busy"
                @click="unblockRow(row)"
              >
                <Unlocked16 aria-hidden="true" />
              </IconButton>

              <IconButton
                v-else-if="panel === 'muted'"
                :label="t('relationships.unmute')"
                :disabled="busy"
                @click="unmuteRow(row)"
              >
                <Notification16 aria-hidden="true" />
              </IconButton>
            </div>
          </li>
        </ul>

        <template v-if="total > rows.length" #footer>
          <button
            type="button"
            class="btn btn-outline btn-xs"
            :disabled="loadingMore"
            @click="loadMore"
          >
            {{ loadingMore ? t('common.loading') : t('relationships.loadMore') }}
          </button>
        </template>
      </ConsolePanel>

      <div class="space-y-6">
        <ConsolePanel :title="t('relationships.find')">
          <form @submit.prevent="search">
            <label for="relationship-query" class="field-label">
              {{ t('relationships.query') }}
            </label>
            <div class="flex gap-2">
              <input
                id="relationship-query"
                v-model="query"
                class="input input-sm"
                :placeholder="t('relationships.queryPlaceholder')"
                autocapitalize="off"
                @input="searched = false"
              />
              <button type="submit" class="btn btn-primary btn-sm" :disabled="searching">
                {{ t('common.search') }}
              </button>
            </div>
          </form>

          <p v-if="searchError" class="mt-3 text-xs text-error">{{ searchError }}</p>

          <LoadingSkeleton v-else-if="searching" variant="rows" :rows="2" :rules="false" />

          <template v-else-if="searched">
            <p v-if="!results.length" class="voice mt-4">{{ t('relationships.noResults') }}</p>
            <ul v-else class="mt-3">
              <li
                v-for="account in results"
                :key="account.id"
                class="flex items-start justify-between gap-3 border-b border-line-soft py-2.5 last:border-0"
              >
                <div class="min-w-0">
                  <RouterLink
                    :to="`/@${encodeURIComponent(account.name)}`"
                    class="block truncate text-xs text-base-content hover:underline"
                  >
                    {{ displayName(account) }}
                  </RouterLink>
                  <p class="mt-0.5 text-2xs text-faint">@{{ account.name }}</p>
                  <span v-if="standing(account)" class="chip mt-1.5 text-dim">
                    {{ standing(account) }}
                  </span>
                </div>
                <IconButton
                  v-if="!standing(account)"
                  :label="t('relationships.sendRequest')"
                  :disabled="busy"
                  @click="requestFrom(account)"
                >
                  <UserFollow16 aria-hidden="true" />
                </IconButton>
              </li>
            </ul>
          </template>

          <p v-else class="voice mt-4">{{ t('relationships.searchHint') }}</p>

          <template v-if="query || results.length" #footer>
            <button type="button" class="btn btn-ghost btn-xs" @click="clearSearch">
              {{ t('relationships.clearSearch') }}
            </button>
          </template>
        </ConsolePanel>

        <ConsolePanel
          :title="t('relationships.sentRequests')"
          :note="t('relationships.totalNote', { n: outgoing.length })"
        >
          <p v-if="!outgoing.length" class="voice">{{ t('relationships.emptySent') }}</p>
          <ul v-else class="space-y-2.5">
            <li
              v-for="row in outgoing"
              :key="row.related_id"
              class="flex items-center justify-between gap-3"
            >
              <div class="min-w-0">
                <RouterLink
                  v-if="counterpart(row)"
                  :to="`/@${encodeURIComponent(counterpart(row)!.name)}`"
                  class="block truncate text-xs text-base-content hover:underline"
                >
                  {{ displayName(counterpart(row)) }}
                </RouterLink>
                <p class="text-2xs text-faint">
                  {{
                    row.expired_at
                      ? t('relationships.expiresIn', { time: remaining(row.expired_at) })
                      : t('relationships.pending')
                  }}
                </p>
              </div>
              <IconButton
                :label="t('relationships.cancelRequest')"
                :disabled="busy"
                @click="cancelRow(row)"
              >
                <Close16 aria-hidden="true" />
              </IconButton>
            </li>
          </ul>
        </ConsolePanel>
      </div>
    </div>

    <ConfirmAction
      v-if="pendingAction"
      :title="confirmCopy.title"
      :body="confirmCopy.body"
      :action="confirmCopy.action"
      tone="error"
      :busy="busy"
      @cancel="pendingAction = null"
      @confirm="confirmAction"
    />
  </div>
</template>
