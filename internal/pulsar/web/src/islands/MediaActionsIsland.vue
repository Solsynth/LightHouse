<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Favorite16, FavoriteFilled16 } from '@carbon/icons-vue'
import { request, requestList, formatTimestamp, type Collection, type Review } from '@/lib/api'

const props = defineProps<{
  catalogId: string
  urn: string
  title: string
  domain: string
  signedIn?: boolean
}>()

const reviews = ref<Review[]>([])
const avgRating = ref<number | null>(null)
const reviewCount = ref(0)
const collections = ref<Collection[]>([])
const selectedCollection = ref('')
const isFavourited = ref(false)

const myRating = ref(8)
const myBody = ref('')
const saving = ref(false)
const feedback = ref('')

async function loadPublic() {
  try {
    const [revRes, statsRes] = await Promise.all([
      requestList<Review>('/api/reviews', { catalog_id: props.catalogId, take: 20 }),
      request<{ average_rating?: number; count?: number }>('/api/reviews/stats', {
        query: { catalog_id: props.catalogId },
      }).catch(() => ({}) as { average_rating?: number; count?: number }),
    ])
    reviews.value = revRes.items
    avgRating.value = statsRes.average_rating ?? null
    reviewCount.value = statsRes.count ?? revRes.total
  } catch {
    // ignore
  }
}

async function loadSignedIn() {
  if (!props.signedIn) return
  try {
    const [colRes, myRev] = await Promise.all([
      requestList<Collection>('/api/collections', { take: 50 }),
      request<Review>('/api/reviews/me', { query: { catalog_id: props.catalogId } }).catch(() => null),
    ])
    collections.value = colRes.items
    if (myRev) {
      myRating.value = myRev.rating ?? 0
      myBody.value = myRev.body ?? ''
    }
  } catch {
    // ignore
  }
}

async function toggleFavourite() {
  if (!props.signedIn) return
  try {
    if (isFavourited.value) {
      await request(`/api/collections/favourites/${props.catalogId}`, { method: 'DELETE' })
      isFavourited.value = false
    } else {
      await request('/api/collections/favourites', {
        method: 'PUT',
        body: { catalog_id: props.catalogId, domain: props.domain, title: props.title, urn: props.urn },
      })
      isFavourited.value = true
    }
  } catch (err) {
    feedback.value = err instanceof Error ? err.message : 'Failed to update favourite'
  }
}

async function addToCollection() {
  if (!selectedCollection.value) return
  try {
    await request(`/api/collections/${selectedCollection.value}/items`, {
      method: 'POST',
      body: { catalog_id: props.catalogId, urn: props.urn, title: props.title },
    })
    feedback.value = 'Added to collection.'
  } catch (err) {
    feedback.value = err instanceof Error ? err.message : 'Failed to add to collection'
  }
}

async function submitReview() {
  saving.value = true
  feedback.value = ''
  try {
    await request('/api/reviews', {
      method: 'PUT',
      body: { catalog_id: props.catalogId, rating: myRating.value, body: myBody.value.trim() || undefined },
    })
    feedback.value = 'Review saved.'
    await loadPublic()
  } catch (err) {
    feedback.value = err instanceof Error ? err.message : 'Failed to save review'
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  void loadPublic()
  void loadSignedIn()
})
</script>

<template>
  <div class="space-y-6">
    <div v-if="signedIn" class="rounded-2xl bg-base-200 p-5 flex flex-wrap items-center gap-3">
      <button type="button" class="btn btn-sm rounded-xl" :class="isFavourited ? 'btn-primary' : 'btn-ghost bg-base-100'" @click="toggleFavourite">
        <FavoriteFilled16 v-if="isFavourited" class="h-4 w-4" />
        <Favorite16 v-else class="h-4 w-4" />
        {{ isFavourited ? 'Favourited' : 'Favourite' }}
      </button>

      <div v-if="collections.length" class="flex items-center gap-2 flex-1 min-w-[220px]">
        <select v-model="selectedCollection" class="select select-bordered select-sm bg-base-100 rounded-xl flex-1">
          <option value="" disabled>Add to collection…</option>
          <option v-for="c in collections" :key="c.id" :value="c.id">{{ c.name }}</option>
        </select>
        <button type="button" class="btn btn-sm btn-primary rounded-xl" :disabled="!selectedCollection" @click="addToCollection">
          Add
        </button>
      </div>
      <p v-if="feedback" class="w-full text-xs text-primary">{{ feedback }}</p>
    </div>

    <section class="rounded-2xl bg-base-200 p-5 space-y-4">
      <div class="flex items-center justify-between">
        <h2 class="text-sm font-mono uppercase tracking-wider text-[var(--console-faint)]">
          Community Reviews ({{ reviewCount }})
        </h2>
        <span v-if="avgRating" class="badge badge-primary font-mono">{{ avgRating.toFixed(1) }} / 10</span>
      </div>

      <form v-if="signedIn" class="rounded-xl bg-base-100 p-4 space-y-3" @submit.prevent="submitReview">
        <div class="flex items-center justify-between gap-4">
          <label class="text-xs font-mono uppercase tracking-wider text-[var(--console-dim)]">Your Rating: {{ myRating }}/10</label>
          <input v-model.number="myRating" type="range" min="1" max="10" class="range range-primary range-xs w-40" />
        </div>
        <textarea v-model="myBody" rows="2" placeholder="Write a short review…" class="textarea textarea-bordered w-full bg-base-200 rounded-xl text-sm"></textarea>
        <div class="flex justify-end">
          <button type="submit" class="btn btn-primary btn-sm rounded-xl" :disabled="saving">Save Review</button>
        </div>
      </form>

      <p v-if="reviews.length === 0" class="text-sm text-[var(--console-faint)]">No reviews yet.</p>
      <div v-else class="space-y-3">
        <div v-for="r in reviews" :key="r.id" class="rounded-xl bg-base-100 p-4 space-y-1.5">
          <div class="flex items-center justify-between">
            <span class="badge badge-sm badge-ghost font-mono">{{ r.rating }} / 10</span>
            <span class="text-[11px] font-mono text-[var(--console-faint)]">{{ formatTimestamp(r.updated_at) }}</span>
          </div>
          <p v-if="r.body" class="text-sm text-[var(--console-dim)] whitespace-pre-line">{{ r.body }}</p>
        </div>
      </div>
    </section>
  </div>
</template>
