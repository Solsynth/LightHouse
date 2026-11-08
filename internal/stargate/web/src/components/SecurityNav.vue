<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'

import TabBox from '@/components/TabBox.vue'
import type { Tab } from '@/components/tab'
import { MODULES } from '@/lib/modules'
import { useModulesStore } from '@/stores/modules'

const { t } = useI18n()
const route = useRoute()
const modules = useModulesStore()

const allTabs = computed<Tab[]>(() => [
  { key: '/security/sessions', to: '/security/sessions', label: t('securityNav.sessions') },
  { key: '/security/devices', to: '/security/devices', label: t('securityNav.devices') },
  { key: '/security/factors', to: '/security/factors', label: t('securityNav.factors') },
  { key: '/security/passkeys', to: '/security/passkeys', label: t('securityNav.passkeys') },
  { key: '/security/keys', to: '/security/keys', label: t('securityNav.keys') },
  { key: '/security/contacts', to: '/security/contacts', label: t('securityNav.contacts') },
  {
    key: '/security/connections',
    to: '/security/connections',
    label: t('securityNav.connections'),
  },
  {
    key: '/security/preferences',
    to: '/security/preferences',
    label: t('securityNav.preferences'),
  },
])

// The public-key page belongs to the optional keys module; a deployment
// without it has no route and must not show the tab.
const tabs = computed<Tab[]>(() =>
  modules.hasAll([MODULES.keys])
    ? allTabs.value
    : allTabs.value.filter((tab) => tab.key !== '/security/keys'),
)
</script>

<template>
  <TabBox :tabs="tabs" :active="route.path" :label="t('securityNav.aria')" />
</template>
