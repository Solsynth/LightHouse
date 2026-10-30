<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'

import TabBox from '@/components/TabBox.vue'
import type { Tab } from '@/components/tab'

const { t } = useI18n()
const route = useRoute()

const tabs = computed<Tab[]>(() => [
  { key: '/security/sessions', to: '/security/sessions', label: t('securityNav.sessions') },
  { key: '/security/devices', to: '/security/devices', label: t('securityNav.devices') },
  { key: '/security/factors', to: '/security/factors', label: t('securityNav.factors') },
  { key: '/security/passkeys', to: '/security/passkeys', label: t('securityNav.passkeys') },
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
</script>

<template>
  <TabBox :tabs="tabs" :active="route.path" :label="t('securityNav.aria')" />
</template>
