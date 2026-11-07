<script setup lang="ts">
import { computed, inject } from 'vue'
import { useRoute } from 'vue-router'

import { TICKET_REFRESH } from '@/lib/tickets'
import TicketWorkspace from '@/components/tickets/TicketWorkspace.vue'

const route = useRoute()
const refresh = inject(TICKET_REFRESH, async () => {})
const id = computed(() => String(route.params.id ?? ''))
</script>

<template>
  <TicketWorkspace :id="id" @changed="refresh" />
</template>
