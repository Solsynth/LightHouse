<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute } from 'vue-router'

const { t } = useI18n()
const route = useRoute()

const tabs = computed(() => [
  { to: '/security/sessions', label: t('securityNav.sessions') },
  { to: '/security/factors', label: t('securityNav.factors') },
  { to: '/security/contacts', label: t('securityNav.contacts') },
  { to: '/security/preferences', label: t('securityNav.preferences') },
])
</script>

<template>
  <nav
    class="mb-6 flex flex-wrap gap-x-5 gap-y-2 border-b border-line pb-3"
    :aria-label="t('securityNav.aria')"
  >
    <RouterLink
      v-for="tab in tabs"
      :key="tab.to"
      :to="tab.to"
      class="text-2xs tracking-[0.02em] transition-colors"
      :class="
        route.path === tab.to
          ? 'text-base-content underline decoration-primary decoration-1 underline-offset-[7px]'
          : 'text-faint hover:text-dim'
      "
    >
      {{ tab.label }}
    </RouterLink>
  </nav>
</template>
