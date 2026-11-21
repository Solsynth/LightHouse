<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Add16, TrashCan16 } from '@carbon/icons-vue'
import { request, requestList, DOMAINS, type Collection, type CollectionItem } from '@/lib/api'

const tab = ref<'collections' | 'favourites'>('collections')
const collections = ref<Collection[]>([])
const favourites = ref<CollectionItem[]>([])
const loading = ref(true)
const error = ref('')

const showCreate = ref(false)
const newName = ref('')
const newDomain = ref('music')
const newKind = ref('playlist')
const newDesc = ref('')
const newPublic = ref(true)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [colRes, favRes] = await Promise.all([
      requestList<Collection>('/api/collections', { take: 50 }),
      request<{ items?: CollectionItem[] } | CollectionItem[]>('/api/collections/favourites').catch(() => []),
    ])
    collections.value = colRes.items
    favourites.value = Array.isArray(favRes) ? favRes : (favRes.items ?? [])
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to load collections'
  } finally {
    loading.value = false
  }
}

async function createCollection() {
  if (!newName.value.trim()) return
  try {
    const created = await request<Collection>('/api/collections', {
      method: 'POST',
      body: {
        name: newName.value.trim(),
        domain: newDomain.value,
        kind: newKind.value,
        description: newDesc.value.trim() || undefined,
        is_public: newPublic.value,
      },
    })
    collections.value.unshift(created)
    newName.value = ''
    newDesc.value = ''
    showCreate.value = false
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Create failed'
  }
}

async function deleteCollection(id: string) {
  try {
    await request(`/api/collections/${id}`, { method: 'DELETE' })
    collections.value = collections.value.filter((c) => c.id !== id)
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Delete failed'
  }
}

async function removeFavourite(catalogId: string) {
  try {
    await request(`/api/collections/favourites/${catalogId}`, { method: 'DELETE' })
    favourites.value = favourites.value.filter((f) => (f.catalog_id ?? f.catalog_item?.id ?? f.catalog?.id) !== catalogId)
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to remove favourite'
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-5">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="tabs tabs-boxed bg-base-200 rounded-xl p-1">
        <button
          type="button"
          class="tab rounded-lg"
          :class="{ 'tab-active': tab === 'collections' }"
          @click="tab = 'collections'"
        >
          Collections ({{ collections.length }})
        </button>
        <button
          type="button"
          class="tab rounded-lg"
          :class="{ 'tab-active': tab === 'favourites' }"
          @click="tab = 'favourites'"
        >
          Favourites ({{ favourites.length }})
        </button>
      </div>
      <button v-if="tab === 'collections'" type="button" class="btn btn-primary btn-sm rounded-xl" @click="showCreate = !showCreate">
        <Add16 class="h-4 w-4" />
        New Collection
      </button>
    </div>

    <div v-if="error" class="alert alert-error rounded-xl text-sm">{{ error }}</div>

    <form v-if="showCreate" class="rounded-2xl bg-base-200 p-5 space-y-4" @submit.prevent="createCollection">
      <h2 class="font-semibold">Create Collection</h2>
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
        <input v-model="newName" type="text" placeholder="Name" required class="input input-bordered bg-base-100 rounded-xl sm:col-span-1" />
        <select v-model="newDomain" class="select select-bordered bg-base-100 rounded-xl capitalize">
          <option v-for="d in DOMAINS" :key="d" :value="d">{{ d }}</option>
        </select>
        <select v-model="newKind" class="select select-bordered bg-base-100 rounded-xl capitalize">
          <option value="playlist">Playlist</option>
          <option value="watchlist">Watchlist</option>
          <option value="backlog">Backlog</option>
          <option value="custom">Custom</option>
        </select>
      </div>
      <textarea v-model="newDesc" rows="2" placeholder="Description (optional)" class="textarea textarea-bordered w-full bg-base-100 rounded-xl"></textarea>
      <div class="flex items-center justify-between">
        <label class="label cursor-pointer gap-2">
          <input v-model="newPublic" type="checkbox" class="toggle toggle-primary toggle-sm" />
          <span class="label-text text-sm">Publicly shareable</span>
        </label>
        <div class="flex gap-2">
          <button type="button" class="btn btn-ghost btn-sm rounded-xl" @click="showCreate = false">Cancel</button>
          <button type="submit" class="btn btn-primary btn-sm rounded-xl">Create</button>
        </div>
      </div>
    </form>

    <div v-if="loading" class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
      <div v-for="n in 3" :key="n" class="skeleton h-28 rounded-2xl"></div>
    </div>

    <template v-else-if="tab === 'collections'">
      <div v-if="collections.length === 0" class="rounded-2xl bg-base-200 p-8 text-center text-sm text-[var(--console-faint)]">
        No collections yet. Create a playlist or watchlist to get started.
      </div>
      <div v-else class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
        <div v-for="c in collections" :key="c.id" class="rounded-2xl bg-base-200 p-5 flex flex-col justify-between gap-4">
          <a :href="`/c/${c.id}`" class="block space-y-2 group">
            <div class="flex items-center justify-between gap-2">
              <span class="badge badge-sm badge-primary uppercase">{{ c.domain }}</span>
              <span class="text-xs font-mono text-[var(--console-faint)]">
                {{ c.kind }} · {{ c.is_public ? 'Public' : 'Private' }}
              </span>
            </div>
            <h3 class="font-semibold text-base group-hover:text-primary transition-colors truncate">{{ c.name }}</h3>
            <p v-if="c.description" class="text-xs text-[var(--console-dim)] line-clamp-2">{{ c.description }}</p>
          </a>
          <div class="flex items-center justify-between pt-2 border-t border-[var(--console-line-soft)]">
            <a :href="`/c/${c.id}`" class="text-xs text-primary hover:underline">Open</a>
            <button type="button" class="btn btn-ghost btn-xs text-error rounded-lg" @click="deleteCollection(c.id)">
              <TrashCan16 class="h-3.5 w-3.5" />
            </button>
          </div>
        </div>
      </div>
    </template>

    <template v-else>
      <div v-if="favourites.length === 0" class="rounded-2xl bg-base-200 p-8 text-center text-sm text-[var(--console-faint)]">
        No favourites yet.
      </div>
      <div v-else class="rounded-2xl bg-base-200 divide-y divide-[var(--console-line-soft)]">
        <div v-for="f in favourites" :key="f.id" class="p-4 flex items-center gap-3.5">
          <div class="min-w-0 flex-1">
            <p class="font-medium text-sm truncate">{{ (f.catalog_item || f.catalog)?.title ?? f.title ?? 'Untitled' }}</p>
            <p class="text-xs text-[var(--console-dim)] truncate">{{ (f.catalog_item || f.catalog)?.subtitle ?? f.subtitle }}</p>
          </div>
          <button
            v-if="f.catalog_id ?? (f.catalog_item || f.catalog)?.id"
            type="button"
            class="btn btn-ghost btn-xs text-error rounded-lg"
            @click="removeFavourite((f.catalog_id ?? (f.catalog_item || f.catalog)?.id)!)"
          >
            <TrashCan16 class="h-4 w-4" />
          </button>
        </div>
      </div>
    </template>
  </div>
</template>
