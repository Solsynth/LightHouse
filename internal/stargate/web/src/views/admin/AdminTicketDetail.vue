<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, RouterLink } from 'vue-router'

import PageHeader from '@/components/PageHeader.vue'
import AdminNav from '@/components/AdminNav.vue'
import TicketWorkspace from '@/components/tickets/TicketWorkspace.vue'

const { t } = useI18n()
const route = useRoute()
const id = computed(() => String(route.params.id ?? ''))
</script>

<template>
  <div>
    <PageHeader
      :title="t('admin.tickets.detailTitle')"
      :description="t('admin.tickets.detailDescription')"
    >
      <template #actions>
        <RouterLink to="/admin/tickets" class="btn btn-ghost btn-sm">
          {{ t('admin.tickets.backToQueue') }}
        </RouterLink>
      </template>
    </PageHeader>

    <AdminNav />

    <TicketWorkspace :id="id" admin />
  </div>
</template>
