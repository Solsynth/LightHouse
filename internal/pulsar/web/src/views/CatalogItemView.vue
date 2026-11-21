<script setup lang="ts">
import { computed, inject, onMounted, ref, watch, type ComputedRef } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { request, type CatalogItem } from '@/lib/api'
import MediaActionsIsland from '@/islands/MediaActionsIsland.vue'
import MediaReviewsIsland from '@/islands/MediaReviewsIsland.vue'

const route = useRoute()
const auth = inject<{ isAuthenticated: ComputedRef<boolean> }>('pulsarAuth')
const itemId = computed(() => String(route.params.id || ''))

const item = ref<CatalogItem | null>(null)
const loading = ref(true)
const error = ref('')

async function loadItem() {
  loading.value = true
  error.value = ''
  try {
    item.value = await request<CatalogItem>(`/api/catalog/${encodeURIComponent(itemId.value)}`)
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to load catalog item'
  } finally {
    loading.value = false
  }
}

onMounted(loadItem)
watch(itemId, loadItem)
</script>

<template>
  <div class="space-y-6">
    <div class="flex items-center gap-2 text-xs text-base-content/60">
      <RouterLink to="/catalog" class="hover:text-primary">← Back to Catalog</RouterLink>
    </div>

    <div v-if="loading" class="py-12 text-center text-sm text-base-content/60">
      Loading media item...
    </div>

    <div v-else-if="error" class="rounded-xl border border-error/40 bg-error/10 p-4 text-sm text-error">
      {{ error }}
    </div>

    <template v-else-if="item">
      <div class="rounded-2xl border border-base-300/60 bg-base-200/50 p-5 flex flex-col sm:flex-row gap-5">
        <img
          v-if="item.artwork_url"
          :data-src="item.artwork_url"
          loading="lazy"
          :alt="item.title"
          class="h-44 w-32 rounded-xl object-cover bg-base-300 shrink-0"
        />
        <div class="min-w-0 flex-1 space-y-2">
          <div class="flex flex-wrap items-center gap-2">
            <span class="badge badge-sm badge-primary uppercase">{{ item.domain }}</span>
            <span v-if="item.caption" class="text-xs font-mono text-base-content/60">{{ item.caption }}</span>
          </div>
          <h1 class="text-2xl font-bold tracking-tight">{{ item.title }}</h1>
          <p v-if="item.subtitle" class="text-sm text-base-content/70">{{ item.subtitle }}</p>
        </div>
      </div>

      <MediaActionsIsland
        :catalog-id="item.id"
        :urn="item.urn || `pulsar:${item.domain}:${item.id}`"
        :title="item.title"
        :domain="item.domain"
        :signed-in="auth?.isAuthenticated.value ?? false"
      />

      <MediaReviewsIsland
        :catalog-id="item.id"
        :signed-in="auth?.isAuthenticated.value ?? false"
      />
    </template>
  </div>
</template>
