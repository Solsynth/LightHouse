<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { TrashCan16 } from '@carbon/icons-vue'
import { request, COLLECTION_STATUSES, type Collection, type CollectionItem } from '@/lib/api'

const props = defineProps<{ collectionId: string; isOwner?: boolean }>()

const collection = ref<Collection | null>(null)
const loading = ref(true)
const error = ref('')
const feedback = ref('')
const viewMode = ref<'shelf' | 'list'>('shelf')

const editingMeta = ref(false)
const editName = ref('')
const editDesc = ref('')
const editPublic = ref(true)
const savingMeta = ref(false)

const editingItemId = ref<string | null>(null)
const itemStatus = ref('')
const itemRating = ref<number | null>(null)
const itemProgress = ref<number | undefined>(undefined)
const itemNote = ref('')

const editingItem = computed(() => collection.value?.items?.find((i) => i.id === editingItemId.value) ?? null)

function statusLabel(status?: string): string {
  switch (status) {
    case 'plan_to_watch':
      return 'Plan to Watch'
    case 'watching':
      return 'Watching'
    case 'completed':
      return 'Completed'
    case 'on_hold':
      return 'On Hold'
    case 'dropped':
      return 'Dropped'
    default:
      return status ? status.replace(/_/g, ' ') : ''
  }
}

function statusDotClass(status?: string): string {
  switch (status) {
    case 'watching':
      return 'bg-primary'
    case 'completed':
      return 'bg-success'
    case 'on_hold':
      return 'bg-warning'
    case 'dropped':
      return 'bg-error'
    default:
      return 'bg-[var(--console-faint)]'
  }
}

function itemCatalog(item: CollectionItem) {
  return item.catalog_item || item.catalog
}

function itemHref(item: CollectionItem): string | undefined {
  const cat = itemCatalog(item)
  if (cat?.urn || item.urn) return `/m/${encodeURIComponent((cat?.urn || item.urn)!)}`
  if (item.catalog_id) return `/catalog/${item.catalog_id}`
  return undefined
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const col = await request<Collection>(`/api/collections/${props.collectionId}`)
    collection.value = col
    editName.value = col.name
    editDesc.value = col.description ?? ''
    editPublic.value = col.is_public
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Unable to load collection items'
  } finally {
    loading.value = false
  }
}

async function saveMeta() {
  if (!collection.value || !editName.value.trim()) return
  savingMeta.value = true
  error.value = ''
  try {
    const updated = await request<Collection>(`/api/collections/${collection.value.id}`, {
      method: 'PUT',
      body: {
        name: editName.value.trim(),
        description: editDesc.value.trim(),
        is_public: editPublic.value,
      },
    })
    collection.value = { ...collection.value, ...updated }
    editingMeta.value = false
    feedback.value = 'Collection updated'
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to update collection'
  } finally {
    savingMeta.value = false
  }
}

function startEditItem(item: CollectionItem) {
  if (editingItemId.value === item.id) {
    editingItemId.value = null
    return
  }
  editingItemId.value = item.id
  itemStatus.value = item.status ?? ''
  itemRating.value = item.rating ?? item.score ?? null
  itemProgress.value = item.progress
  itemNote.value = item.note ?? item.notes ?? ''
}

async function saveItem(itemId: string) {
  if (!collection.value) return
  error.value = ''
  try {
    const updated = await request<CollectionItem>(`/api/collections/${collection.value.id}/items/${itemId}`, {
      method: 'PATCH',
      body: {
        status: itemStatus.value || undefined,
        rating: itemRating.value ?? undefined,
        progress: itemProgress.value ?? undefined,
        note: itemNote.value.trim() || undefined,
      },
    })
    collection.value.items = collection.value.items?.map((i) => (i.id === itemId ? { ...i, ...updated } : i))
    editingItemId.value = null
    feedback.value = 'Item updated'
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to update item'
  }
}

async function removeItem(itemId: string) {
  if (!collection.value) return
  try {
    await request(`/api/collections/${collection.value.id}/items/${itemId}`, { method: 'DELETE' })
    collection.value.items = collection.value.items?.filter((i) => i.id !== itemId)
    if (editingItemId.value === itemId) editingItemId.value = null
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to remove item'
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-4">
    <div v-if="error" class="alert alert-error rounded-xl text-sm">{{ error }}</div>
    <div v-if="feedback" class="alert alert-success rounded-xl text-sm">{{ feedback }}</div>

    <div v-if="collection" class="flex flex-wrap items-center justify-between gap-2">
      <div class="inline-flex items-center gap-1 rounded-xl bg-base-200 p-1">
        <button
          type="button"
          class="chip"
          :class="{ 'chip-active': viewMode === 'shelf' }"
          @click="viewMode = 'shelf'"
        >
          Shelf
        </button>
        <button
          type="button"
          class="chip"
          :class="{ 'chip-active': viewMode === 'list' }"
          @click="viewMode = 'list'"
        >
          List
        </button>
      </div>
      <button v-if="isOwner" type="button" class="btn btn-ghost btn-xs bg-base-200 rounded-xl" @click="editingMeta = !editingMeta">
        {{ editingMeta ? 'Close Editor' : 'Edit Collection Details' }}
      </button>
    </div>

    <form v-if="editingMeta && isOwner" class="rounded-2xl bg-base-200 p-5 space-y-3" @submit.prevent="saveMeta">
      <input v-model="editName" type="text" required placeholder="Collection name" class="input w-full" />
      <textarea v-model="editDesc" rows="2" placeholder="Description" class="textarea w-full"></textarea>
      <div class="flex items-center justify-between">
        <label class="label cursor-pointer gap-2 justify-start">
          <input v-model="editPublic" type="checkbox" class="toggle toggle-primary toggle-sm" />
          <span class="label-text text-sm">Publicly visible</span>
        </label>
        <button type="submit" class="btn btn-primary btn-sm" :disabled="savingMeta">
          {{ savingMeta ? 'Saving...' : 'Save Changes' }}
        </button>
      </div>
    </form>

    <form
      v-if="isOwner && editingItem"
      class="rounded-2xl bg-base-200 p-4 space-y-3"
      @submit.prevent="saveItem(editingItem.id)"
    >
      <div class="flex items-center justify-between gap-2">
        <span class="text-sm font-semibold truncate">
          Editing: {{ itemCatalog(editingItem)?.title || editingItem.title || 'Untitled' }}
        </span>
        <button type="button" class="btn btn-ghost btn-xs" @click="editingItemId = null">Cancel</button>
      </div>
      <div class="grid grid-cols-1 sm:grid-cols-4 gap-2.5">
        <select v-model="itemStatus" class="select select-sm w-full">
          <option value="">No status</option>
          <option v-for="st in COLLECTION_STATUSES" :key="st" :value="st">{{ statusLabel(st) }}</option>
        </select>
        <select v-model.number="itemRating" class="select select-sm w-full font-mono">
          <option :value="null">No rating</option>
          <option v-for="n in 10" :key="n" :value="n">{{ n }}/10</option>
        </select>
        <input v-model.number="itemProgress" type="number" min="0" placeholder="Progress (ep/ch)" class="input input-sm w-full" />
        <input v-model="itemNote" type="text" placeholder="Note" class="input input-sm w-full" />
      </div>
      <div class="flex items-center justify-between">
        <button type="button" class="btn btn-ghost btn-xs text-error" @click="removeItem(editingItem.id)">
          <TrashCan16 class="w-3.5 h-3.5" />
          Remove from collection
        </button>
        <button type="submit" class="btn btn-primary btn-sm">Save Status</button>
      </div>
    </form>

    <div v-if="loading" class="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-5 gap-4">
      <div v-for="n in 5" :key="n" class="skeleton aspect-[2/3] rounded-2xl"></div>
    </div>
    <div v-else-if="!collection?.items?.length" class="rounded-2xl bg-base-200 p-8 text-center text-sm text-[var(--console-faint)]">
      This collection has no items yet.
    </div>

    <div v-else-if="viewMode === 'shelf'" class="grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 gap-4">
      <div v-for="item in collection.items" :key="item.id" class="group space-y-2">
        <div class="relative aspect-[2/3] w-full overflow-hidden rounded-2xl bg-base-200 ring-1 ring-[var(--console-line-soft)]">
          <a v-if="itemHref(item)" :href="itemHref(item)" class="block h-full w-full">
            <img
              v-if="itemCatalog(item)?.artwork_url || item.artwork_url"
              :src="itemCatalog(item)?.artwork_url || item.artwork_url"
              :alt="itemCatalog(item)?.title || item.title"
              class="h-full w-full object-cover group-hover:scale-[1.03] transition-transform duration-200"
              loading="lazy"
            />
            <div v-else class="h-full w-full flex items-center justify-center p-3 text-center text-xs font-mono text-[var(--console-faint)]">
              {{ itemCatalog(item)?.title || item.title || 'Untitled' }}
            </div>
          </a>
          <template v-else>
            <img
              v-if="itemCatalog(item)?.artwork_url || item.artwork_url"
              :src="itemCatalog(item)?.artwork_url || item.artwork_url"
              class="h-full w-full object-cover"
              loading="lazy"
            />
          </template>

          <span
            v-if="item.rating || item.score"
            class="absolute top-2 left-2 inline-flex items-center gap-1 rounded-full bg-base-100/80 backdrop-blur-sm px-2 py-0.5 text-[11px] font-mono font-semibold text-base-content"
          >
            ★ {{ item.rating || item.score }}
          </span>

          <button
            v-if="isOwner"
            type="button"
            class="absolute top-2 right-2 inline-flex h-7 px-2 items-center justify-center rounded-full bg-base-100/85 backdrop-blur-sm text-[11px] font-medium text-base-content opacity-90 hover:bg-primary hover:text-primary-content transition-colors"
            @click="startEditItem(item)"
          >
            Edit
          </button>
          <span
            v-else-if="item.note || item.notes"
            :title="item.note || item.notes"
            class="absolute top-2 right-2 inline-flex h-6 w-6 items-center justify-center rounded-full bg-base-100/80 backdrop-blur-sm text-[11px] text-base-content"
          >
            ✎
          </span>

          <span
            v-if="item.status"
            class="absolute bottom-2 left-2 inline-flex items-center gap-1.5 rounded-full bg-base-100/85 backdrop-blur-sm px-2.5 py-0.5 text-[11px] font-medium text-base-content"
          >
            <span class="h-1.5 w-1.5 rounded-full" :class="statusDotClass(item.status)"></span>
            <span>{{ statusLabel(item.status) }}</span>
          </span>
        </div>
        <div class="min-w-0">
          <a
            v-if="itemHref(item)"
            :href="itemHref(item)"
            class="block text-sm font-semibold truncate hover:text-primary transition-colors"
          >
            {{ itemCatalog(item)?.title || item.title || 'Untitled' }}
          </a>
          <p v-else class="text-sm font-semibold truncate">{{ itemCatalog(item)?.title || item.title || 'Untitled' }}</p>
          <p v-if="itemCatalog(item)?.subtitle || item.subtitle" class="text-xs text-[var(--console-dim)] truncate">
            {{ itemCatalog(item)?.subtitle || item.subtitle }}
          </p>
          <p v-if="item.note || item.notes" class="mt-1 text-xs text-[var(--console-faint)] line-clamp-2">
            {{ item.note || item.notes }}
          </p>
        </div>
      </div>
    </div>

    <div v-else class="rounded-2xl bg-base-200 divide-y divide-[var(--console-line-soft)]">
      <div v-for="(item, idx) in collection.items" :key="item.id" class="p-4 flex items-center gap-3.5">
        <span class="w-6 text-center text-xs font-mono text-[var(--console-faint)]">{{ idx + 1 }}</span>
        <img
          v-if="itemCatalog(item)?.artwork_url || item.artwork_url"
          :src="itemCatalog(item)?.artwork_url || item.artwork_url"
          class="h-11 w-11 rounded-lg object-cover bg-base-300 shrink-0"
        />
        <div class="min-w-0 flex-1">
          <a
            v-if="itemHref(item)"
            :href="itemHref(item)"
            class="font-medium text-sm hover:text-primary transition-colors truncate block"
          >
            {{ itemCatalog(item)?.title || item.title || 'Untitled' }}
          </a>
          <p v-else class="font-medium text-sm truncate">{{ itemCatalog(item)?.title || item.title || 'Untitled' }}</p>
          <p v-if="itemCatalog(item)?.subtitle || item.subtitle" class="text-xs text-[var(--console-dim)] truncate">
            {{ itemCatalog(item)?.subtitle || item.subtitle }}
          </p>
          <p v-if="item.note || item.notes" class="text-xs text-[var(--console-faint)] mt-1">
            {{ item.note || item.notes }}
          </p>
        </div>
        <span v-if="item.status" class="chip">
          <span class="h-1.5 w-1.5 rounded-full" :class="statusDotClass(item.status)"></span>
          {{ statusLabel(item.status) }}
        </span>
        <span v-if="item.rating || item.score" class="text-xs font-mono text-primary">{{ item.rating || item.score }}/10</span>
        <template v-if="isOwner">
          <button type="button" class="btn btn-ghost btn-xs rounded-lg" @click="startEditItem(item)">
            Edit
          </button>
          <button type="button" class="btn btn-ghost btn-xs text-error rounded-lg" title="Remove item" @click="removeItem(item.id)">
            <TrashCan16 class="w-3.5 h-3.5" />
          </button>
        </template>
      </div>
    </div>
  </div>
</template>
