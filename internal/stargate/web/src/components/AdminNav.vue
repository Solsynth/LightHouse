<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'

import TabBox from '@/components/TabBox.vue'
import type { Tab } from '@/components/tab'

/**
 * The admin section's tab strip. Paths, not local keys: every entry is a route,
 * so the strip doubles as the console's map of the admin surface.
 */
const { t } = useI18n()
const route = useRoute()

const tabs = computed<Tab[]>(() => [
  { key: '/admin', to: '/admin', label: t('adminNav.overview') },
  { key: '/admin/accounts', to: '/admin/accounts', label: t('adminNav.accounts') },
  { key: '/admin/permissions', to: '/admin/permissions', label: t('adminNav.permissions') },
  { key: '/admin/audit', to: '/admin/audit', label: t('adminNav.audit') },
  { key: '/admin/comms', to: '/admin/comms', label: t('adminNav.comms') },
  { key: '/admin/realms', to: '/admin/realms', label: t('adminNav.realms') },
  { key: '/admin/tickets', to: '/admin/tickets', label: t('adminNav.tickets') },
  { key: '/admin/progression', to: '/admin/progression', label: t('adminNav.progression') },
  { key: '/admin/wallet', to: '/admin/wallet', label: t('adminNav.wallet') },
  { key: '/admin/cache', to: '/admin/cache', label: t('adminNav.cache') },
  { key: '/admin/geography', to: '/admin/geography', label: t('adminNav.geography') },
])

const active = computed(() => {
  if (route.path.startsWith('/admin/accounts')) return '/admin/accounts'
  if (route.path.startsWith('/admin/permissions')) return '/admin/permissions'
  if (route.path.startsWith('/admin/realms')) return '/admin/realms'
  if (route.path.startsWith('/admin/tickets')) return '/admin/tickets'
  return route.path
})
</script>

<template>
  <TabBox :tabs="tabs" :active="active" :label="t('adminNav.aria')" />
</template>
