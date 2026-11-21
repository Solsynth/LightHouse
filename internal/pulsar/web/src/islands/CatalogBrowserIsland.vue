<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Add16, Search16 } from '@carbon/icons-vue'
import { request, requestList, DOMAINS, type CatalogItem, type Creator } from '@/lib/api'

const props = defineProps<{ signedIn?: boolean }>()

const tab = ref<'items' | 'creators'>('items')
const query = ref('')
const domain = ref('')
const selectedCreator = ref<Creator | null>(null)
const items = ref<CatalogItem[]>([])
const creators = ref<Creator[]>([])
const total = ref(0)
const loading = ref(true)
const error = ref('')

const showCreate = ref(false)
const newTitle = ref('')
const newSubtitle = ref('')
const newDomain = ref('music')
const newArtwork = ref('')
const newDuration = ref<number | undefined>(undefined)
const newCreator = ref('')
const creating = ref(false)

async function loadItems() {
  loading.value = true
  error.value = ''
  try {
    const res = await requestList<CatalogItem>('/api/catalog', {
      q: query.value.trim() || undefined,
      domain: domain.value || undefined,
      creator_id: selectedCreator.value?.id,
      take: 30,
    })
    items.value = res.items
    total.value = res.total
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to load catalog'
  } finally {
    loading.value = false
  }
}

async function loadCreators() {
  loading.value = true
  error.value = ''
  try {
    const res = await requestList<Creator>('/api/creators', {
      q: query.value.trim() || undefined,
      domain: domain.value || undefined,
      take: 30,
    })
    creators.value = res.items
    total.value = res.total
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to load creators'
  } finally {
    loading.value = false
  }
}

function search() {
  if (tab.value === 'items') {
    void loadItems()
  } else {
    void loadCreators()
  }
}

function switchTab(next: 'items' | 'creators') {
  tab.value = next
  search()
}

function selectCreator(c: Creator) {
  selectedCreator.value = c
  tab.value = 'items'
  void loadItems()
}

function clearCreator() {
  selectedCreator.value = null
  void loadItems()
}

async function createItem() {
  if (!newTitle.value.trim()) return
  creating.value = true
  error.value = ''
  try {
    const created = await request<CatalogItem>('/api/catalog', {
      method: 'POST',
      body: {
        domain: newDomain.value,
        title: newTitle.value.trim(),
        subtitle: newSubtitle.value.trim() || undefined,
        artwork_url: newArtwork.value.trim() || undefined,
        duration_seconds: newDuration.value || undefined,
        creators: newCreator.value.trim() ? [{ name: newCreator.value.trim(), role: 'creator' }] : undefined,
      },
    })
    items.value.unshift(created)
    newTitle.value = ''
    newSubtitle.value = ''
    newArtwork.value = ''
    newDuration.value = undefined
    newCreator.value = ''
    showCreate.value = false
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to create catalog item'
  } finally {
    creating.value = false
  }
}

onMounted(loadItems)
</script>

<template>
  <div class="space-y-5">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="tabs tabs-boxed bg-base-200 rounded-xl p-1">
        <button type="button" class="tab rounded-lg" :class="{ 'tab-active': tab === 'items' }" @click="switchTab('items')">
          Catalog Items
        </button>
        <button type="button" class="tab rounded-lg" :class="{ 'tab-active': tab === 'creators' }" @click="switchTab('creators')">
          Creators
        </button>
      </div>
      <button v-if="props.signedIn" type="button" class="btn btn-primary btn-sm rounded-xl" @click="showCreate = !showCreate">
        <Add16 class="w-4 h-4" />
        New Catalog Item
      </button>
    </div>

    <form v-if="showCreate" class="rounded-2xl bg-base-200 p-5 space-y-4" @submit.prevent="createItem">
      <h3 class="font-semibold">Add Custom Catalog Entry</h3>
      <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
        <input v-model="newTitle" type="text" required placeholder="Title" class="input input-bordered rounded-xl w-full" />
        <select v-model="newDomain" class="select select-bordered rounded-xl w-full">
          <option v-for="d in DOMAINS" :key="d" :value="d">{{ d }}</option>
        </select>
        <input v-model="newSubtitle" type="text" placeholder="Subtitle / Artist / Studio" class="input input-bordered rounded-xl w-full" />
        <input v-model="newCreator" type="text" placeholder="Creator name (optional)" class="input input-bordered rounded-xl w-full" />
        <input v-model="newArtwork" type="url" placeholder="Artwork URL (optional)" class="input input-bordered rounded-xl w-full" />
        <input v-model.number="newDuration" type="number" min="0" placeholder="Duration in seconds (optional)" class="input input-bordered rounded-xl w-full" />
      </div>
      <div class="flex justify-end gap-2">
        <button type="button" class="btn btn-ghost btn-sm rounded-xl" @click="showCreate = false">Cancel</button>
        <button type="submit" class="btn btn-primary btn-sm rounded-xl" :disabled="creating">
          {{ creating ? 'Creating...' : 'Create Entry' }}
        </button>
      </div>
    </form>

    <form class="flex flex-col sm:flex-row gap-2" @submit.prevent="search">
      <div class="relative flex-1">
        <Search16 class="w-4 h-4 text-[var(--console-faint)] absolute left-3.5 top-1/2 -translate-y-1/2 pointer-events-none" />
        <input
          v-model="query"
          type="text"
          :placeholder="tab === 'items' ? 'Search catalog titles...' : 'Search artists, studios, authors...'"
          class="input input-bordered w-full pl-10 rounded-xl bg-base-200"
        />
      </div>
      <select v-model="domain" class="select select-bordered rounded-xl bg-base-200" @change="search">
        <option value="">All domains</option>
        <option v-for="d in DOMAINS" :key="d" :value="d">{{ d }}</option>
      </select>
      <button type="submit" class="btn btn-primary rounded-xl">Search</button>
    </form>

    <div v-if="selectedCreator && tab === 'items'" class="flex items-center gap-2 text-xs">
      <span class="badge badge-primary gap-1.5">
        Creator: {{ selectedCreator.name }}
        <button type="button" class="hover:opacity-75" @click="clearCreator">×</button>
      </span>
    </div>

    <div v-if="error" class="alert alert-error rounded-xl text-sm">{{ error }}</div>

    <div v-if="loading" class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
      <div v-for="n in 6" :key="n" class="skeleton h-20 rounded-2xl"></div>
    </div>

    <template v-else-if="tab === 'items'">
      <p v-if="items.length === 0" class="rounded-2xl bg-base-200 p-8 text-center text-sm text-[var(--console-faint)]">
        No catalog entries matched.
      </p>
      <div v-else class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
        <a
          v-for="item in items"
          :key="item.id"
          :href="`/catalog/${item.id}`"
          class="rounded-2xl bg-base-200 p-3.5 flex items-center gap-3 hover:bg-base-300/70 transition-colors"
        >
          <img
            v-if="item.artwork_url"
            :src="item.artwork_url"
            :alt="item.title"
            class="h-12 w-12 rounded-xl object-cover bg-base-300 shrink-0"
          />
          <div v-else class="h-12 w-12 rounded-xl bg-base-300 flex items-center justify-center text-[10px] font-mono text-[var(--console-faint)] shrink-0">
            {{ item.domain }}
          </div>
          <div class="min-w-0 flex-1">
            <p class="font-medium text-sm truncate">{{ item.title }}</p>
            <p v-if="item.subtitle" class="text-xs text-[var(--console-dim)] truncate">{{ item.subtitle }}</p>
            <div class="flex items-center gap-2 mt-1">
              <span class="badge badge-xs badge-ghost uppercase">{{ item.domain }}</span>
              <span v-if="item.provider" class="text-[11px] font-mono text-[var(--console-faint)]">{{ item.provider }}</span>
            </div>
          </div>
        </a>
      </div>
    </template>

    <template v-else>
      <p v-if="creators.length === 0" class="rounded-2xl bg-base-200 p-8 text-center text-sm text-[var(--console-faint)]">
        No creators matched.
      </p>
      <div v-else class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
        <button
          v-for="c in creators"
          :key="c.id"
          type="button"
          class="rounded-2xl bg-base-200 p-4 text-left hover:bg-base-300/70 transition-colors space-y-1"
          @click="selectCreator(c)"
        >
          <p class="font-semibold text-sm truncate">{{ c.name }}</p>
          <div class="flex items-center justify-between text-xs text-[var(--console-dim)]">
            <span class="capitalize">{{ c.role || c.domain || 'Creator' }}</span>
            <span v-if="c.item_count !== undefined" class="font-mono">{{ c.item_count }} items</span>
          </div>
        </button>
      </div>
    </template>
  </div>
</template>
