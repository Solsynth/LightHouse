<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import type { Component } from 'vue'
import { CircleDash16, Folder16, StoragePool16, TrashCan16, WarningAlt16 } from '@carbon/icons-vue'

import { formatBytes, formatCount } from '@/lib/format'
import { useUiStore } from '@/stores/ui'
import { useVaultStore } from '@/stores/vault'

const ui = useUiStore()
const { t } = useI18n()
const vault = useVaultStore()
const route = useRoute()
const router = useRouter()

interface NavItem {
  label: string
  to: string
  icon: Component
  match: string
  badge?: string
}

const nav = computed<NavItem[]>(() => [
  {
    label: t('nav.drive'),
    to: '/drive',
    icon: Folder16,
    match: '/drive',
    badge: vault.fileCount > 0 ? formatCount(vault.fileCount) : undefined,
  },
  { label: t('nav.unindexed'), to: '/unindexed', icon: WarningAlt16, match: '/unindexed' },
  { label: t('nav.recycle'), to: '/recycle', icon: TrashCan16, match: '/recycle' },
])

function isCurrent(item: NavItem): boolean {
  return route.path === item.to || (item.match === '/drive' && route.path.startsWith('/drive'))
}

const storageCurrent = computed(() => route.path === '/storage')
const usedPercent = computed(() => Math.round(vault.usedFraction * 100))
const usedWidth = computed(() => `${Math.min(100, Math.max(0, usedPercent.value))}%`)

function navigate() {
  ui.sidebarOpen = false
}

function selectPool(poolId: string | null) {
  if (route.meta.mode !== 'drive') {
    void router.push('/drive')
  }
  vault.setPoolFilter(poolId)
  navigate()
}
</script>

<template>
  <button
    v-if="ui.sidebarOpen"
    type="button"
    class="fixed inset-0 z-40 bg-black/55 lg:hidden"
    :aria-label="t('nav.closeNavigation')"
    @click="ui.sidebarOpen = false"
  />

  <aside
    class="fixed inset-y-2 left-2 z-50 flex w-[248px] shrink-0 flex-col overflow-hidden rounded-card bg-base-200 shadow-[0_24px_60px_-28px_rgb(0_0_0/0.5)] transition-transform duration-200 ease-out lg:static lg:inset-auto lg:z-auto lg:h-full lg:translate-x-0 lg:rounded-none lg:bg-transparent lg:shadow-none"
    :class="ui.sidebarOpen ? 'translate-x-0' : '-translate-x-full'"
    :aria-label="t('app.console')"
  >
    <nav class="flex-1 overflow-y-auto px-2 py-3 scroll-region" :aria-label="t('nav.locations')">
      <div class="flex flex-col gap-0.5">
        <RouterLink
          v-for="item in nav"
          :key="item.to"
          :to="item.to"
          class="nav-row h-9"
          :class="isCurrent(item) ? 'nav-row-active' : ''"
          :aria-current="isCurrent(item) ? 'page' : undefined"
          @click="navigate"
        >
          <component
            :is="item.icon"
            class="nav-glyph shrink-0 transition-colors"
            :class="isCurrent(item) ? 'text-primary' : 'text-faint'"
            aria-hidden="true"
          />
          <span class="truncate">{{ item.label }}</span>
          <span v-if="item.badge" class="ml-auto text-2xs tabular-nums text-faint">
            {{ item.badge }}
          </span>
        </RouterLink>
      </div>

      <div class="mt-7">
        <div class="flex items-center justify-between px-3 pb-2">
          <span class="eyebrow">{{ t('nav.pools') }}</span>
          <span class="text-2xs tabular-nums text-faint">{{ vault.pools.length }}</span>
        </div>

        <div class="flex flex-col gap-0.5">
          <button
            type="button"
            class="nav-row h-7 text-left text-2xs"
            :class="vault.poolFilter === null ? 'nav-row-active' : ''"
            @click="selectPool(null)"
          >
            <CircleDash16
              class="shrink-0"
              :class="vault.poolFilter === null ? 'text-primary' : 'text-faint'"
              aria-hidden="true"
            />
            <span>{{ t('nav.allPools') }}</span>
            <span class="ml-auto text-2xs text-faint">{{ t('meta.poolDefault') }}</span>
          </button>

          <button
            v-for="row in vault.poolRows"
            :key="row.pool.id"
            type="button"
            class="nav-row h-7 text-left text-2xs"
            :class="vault.poolFilter === row.pool.id ? 'nav-row-active' : ''"
            :title="t('drive.showPoolObjects', { pool: row.pool.name })"
            @click="selectPool(row.pool.id)"
          >
            <span class="min-w-0 flex-1 truncate">{{ row.pool.name }}</span>
            <span class="shrink-0 text-2xs tabular-nums text-faint">
              {{ formatBytes(row.bytes) }}
            </span>
          </button>
        </div>

        <p
          v-if="vault.poolRows.length === 0 && !vault.volumeLoading"
          class="px-3 py-1 text-2xs text-faint"
        >
          {{ t('storage.noPools') }}
        </p>
      </div>
    </nav>

    <!-- Storage rides in the rail's own tinted well, like Drive's quota
         footer: the fill separates it from the location list, not a rule. -->
    <div class="shrink-0 p-2">
      <div class="rounded-card bg-base-300/35 p-1.5">
        <RouterLink
          to="/storage"
          class="nav-row h-9"
          :class="storageCurrent ? 'nav-row-active' : ''"
          :aria-current="storageCurrent ? 'page' : undefined"
          @click="navigate"
        >
          <StoragePool16
            class="shrink-0"
            :class="storageCurrent ? 'text-primary' : 'text-faint'"
            aria-hidden="true"
          />
          <span>{{ t('nav.storage') }}</span>
          <span class="ml-auto text-2xs tabular-nums text-faint">{{ usedPercent }}%</span>
        </RouterLink>

        <div class="px-3 pb-2 pt-2.5">
          <div class="h-1 w-full overflow-hidden rounded-full bg-base-100/60">
            <div
              class="h-full rounded-full bg-primary transition-[width] duration-700 ease-out"
              :style="{ width: usedWidth }"
            />
          </div>
          <div class="mt-2 flex items-center justify-between text-2xs text-faint">
            <span>{{ formatBytes(vault.usedBytes) }} {{ t('storage.used') }}</span>
            <span>{{ formatBytes(vault.remainingBytes) }} {{ t('storage.remaining') }}</span>
          </div>
        </div>
      </div>
    </div>
  </aside>
</template>
