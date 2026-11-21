<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { TrashCan16 } from '@carbon/icons-vue'
import { request, COLLECTION_STATUSES, type Collection, type CollectionItem } from '@/lib/api'

const props = defineProps<{ collectionId: string; isOwner?: boolean }>()

const collection = ref<Collection | null>(null)
const loading = ref(true)
const error = ref('')
const feedback = ref('')

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

    <div v-if="isOwner && collection" class="flex justify-end">
      <button type="button" class="btn btn-ghost btn-xs bg-base-200 rounded-lg" @click="editingMeta = !editingMeta">
        {{ editingMeta ? 'Close Editor' : 'Edit Collection Details' }}
      </button>
    </div>

    <form v-if="editingMeta && isOwner" class="rounded-2xl bg-base-200 p-5 space-y-3" @submit.prevent="saveMeta">
      <input v-model="editName" type="text" required placeholder="Collection name" class="input input-bordered w-full rounded-xl" />
      <textarea v-model="editDesc" rows="2" placeholder="Description" class="textarea textarea-bordered w-full rounded-xl"></textarea>
      <div class="flex items-center justify-between">
        <label class="label cursor-pointer gap-2 justify-start">
          <input v-model="editPublic" type="checkbox" class="toggle toggle-primary toggle-sm" />
          <span class="label-text text-sm">Publicly visible</span>
        </label>
        <button type="submit" class="btn btn-primary btn-sm rounded-xl" :disabled="savingMeta">
          {{ savingMeta ? 'Saving...' : 'Save Changes' }}
        </button>
      </div>
    </form>

    <div v-if="loading" class="space-y-2">
      <div v-for="n in 4" :key="n" class="skeleton h-14 rounded-xl"></div>
    </div>
    <div v-else-if="!collection?.items?.length" class="rounded-2xl bg-base-200 p-8 text-center text-sm text-[var(--console-faint)]">
      This collection has no items yet.
    </div>
    <div v-else class="rounded-2xl bg-base-200 divide-y divide-[var(--console-line-soft)]">
      <div v-for="(item, idx) in collection.items" :key="item.id" class="p-4 space-y-3">
        <div class="flex items-center gap-3.5">
          <span class="w-6 text-center text-xs font-mono text-[var(--console-faint)]">{{ idx + 1 }}</span>
          <img
            v-if="item.catalog?.artwork_url || item.artwork_url"
            :src="item.catalog?.artwork_url || item.artwork_url"
            class="h-11 w-11 rounded-lg object-cover bg-base-300 shrink-0"
          />
          <div class="min-w-0 flex-1">
            <a
              v-if="item.catalog?.urn || item.urn"
              :href="`/m/${encodeURIComponent((item.catalog?.urn || item.urn)!)}`"
              class="font-medium text-sm hover:text-primary transition-colors truncate block"
            >
              {{ item.catalog?.title || item.title }}
            </a>
            <p v-else class="font-medium text-sm truncate">{{ item.catalog?.title || item.title }}</p>
            <p v-if="item.catalog?.subtitle || item.subtitle" class="text-xs text-[var(--console-dim)] truncate">
              {{ item.catalog?.subtitle || item.subtitle }}
            </p>
            <p v-if="item.note || item.notes" class="text-xs text-[var(--console-faint)] mt-1">
              {{ item.note || item.notes }}
            </p>
          </div>
          <span v-if="item.status" class="badge badge-xs badge-ghost capitalize">{{ item.status.replace(/_/g, ' ') }}</span>
          <span v-if="item.rating || item.score" class="text-xs font-mono text-primary">{{ item.rating || item.score }}/10</span>
          <template v-if="isOwner">
            <button type="button" class="btn btn-ghost btn-xs rounded-lg" @click="editingItemId === item.id ? (editingItemId = null) : startEditItem(item)">
              Edit
            </button>
            <button type="button" class="btn btn-ghost btn-xs text-error rounded-lg" title="Remove item" @click="removeItem(item.id)">
              <TrashCan16 class="w-3.5 h-3.5" />
            </button>
          </template>
        </div>

        <form v-if="isOwner && editingItemId === item.id" class="rounded-xl bg-base-100 p-3 grid grid-cols-1 sm:grid-cols-4 gap-2" @submit.prevent="saveItem(item.id)">
          <select v-model="itemStatus" class="select select-bordered select-xs rounded-lg">
            <option value="">No status</option>
            <option v-for="st in COLLECTION_STATUSES" :key="st" :value="st">{{ st.replace(/_/g, ' ') }}</option>
          </select>
          <select v-model.number="itemRating" class="select select-bordered select-xs rounded-lg font-mono">
            <option :value="null">No rating</option>
            <option v-for="n in 10" :key="n" :value="n">{{ n }}/10</option>
          </select>
          <input v-model.number="itemProgress" type="number" min="0" placeholder="Progress (ep/ch)" class="input input-bordered input-xs rounded-lg" />
          <div class="flex gap-1.5">
            <input v-model="itemNote" type="text" placeholder="Note" class="input input-bordered input-xs rounded-lg flex-1" />
            <button type="submit" class="btn btn-primary btn-xs rounded-lg">Save</button>
          </div>
        </form>
      </div>
    </div>
  </div>
</template>
