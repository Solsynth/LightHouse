<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import CollectionDetailIsland from '@/islands/CollectionDetailIsland.vue'

const route = useRoute()
const collectionId = computed(() => String(route.params.id || ''))
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center gap-2 text-xs text-base-content/60">
      <RouterLink to="/collections" class="hover:text-primary">← Back to Collections</RouterLink>
    </div>
    <CollectionDetailIsland :key="collectionId" :collection-id="collectionId" :is-owner="true" />
  </div>
</template>
