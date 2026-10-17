<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute } from 'vue-router'

const { t } = useI18n()
const route = useRoute()

const tabs = computed(() => [
  { to: '/security/sessions', label: t('securityNav.sessions') },
  { to: '/security/factors', label: t('securityNav.factors') },
  { to: '/security/passkeys', label: t('securityNav.passkeys') },
  { to: '/security/contacts', label: t('securityNav.contacts') },
  { to: '/security/connections', label: t('securityNav.connections') },
  { to: '/security/preferences', label: t('securityNav.preferences') },
])
</script>

<template>
  <nav
    class="mb-6 flex w-fit max-w-full overflow-x-auto border border-line bg-base-200/30"
    :aria-label="t('securityNav.aria')"
  >
    <RouterLink
      v-for="tab in tabs"
      :key="tab.to"
      :to="tab.to"
      class="whitespace-nowrap border-r border-line px-3 py-1.5 text-2xs tracking-[0.02em] transition-colors last:border-r-0"
      :class="
        route.path === tab.to
          ? 'bg-base-300 text-base-content'
          : 'text-faint hover:bg-base-300/50 hover:text-dim'
      "
      :aria-current="route.path === tab.to ? 'page' : undefined"
    >
      {{ tab.label }}
    </RouterLink>
  </nav>
</template>
