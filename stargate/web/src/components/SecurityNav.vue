<script setup lang="ts">
import { RouterLink, useRoute } from 'vue-router'

const route = useRoute()

const tabs = [
  { to: '/security/sessions', label: 'Sessions' },
  { to: '/security/factors', label: 'Sign-In Factors' },
  { to: '/security/contacts', label: 'Contacts' },
  { to: '/security/preferences', label: 'Security Mode' },
]
</script>

<template>
  <nav class="mb-6 flex flex-wrap gap-x-5 gap-y-2 border-b border-line pb-3" aria-label="Security">
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
