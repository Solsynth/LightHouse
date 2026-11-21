<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { TrashCan16 } from '@carbon/icons-vue'
import { request, requestList, formatTimestamp, type Review, type ReviewStats } from '@/lib/api'

const props = defineProps<{
  catalogId: string
  signedIn?: boolean
}>()

const stats = ref<ReviewStats | null>(null)
const reviews = ref<Review[]>([])
const total = ref(0)
const myReview = ref<Review | null>(null)
const rating = ref<number | null>(8)
const body = ref('')
const loading = ref(true)
const saving = ref(false)
const error = ref('')
const feedback = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [statsRes, listRes] = await Promise.all([
      request<ReviewStats>('/api/reviews/stats', { query: { catalog_id: props.catalogId } }).catch(() => null),
      requestList<Review>('/api/reviews', { catalog_id: props.catalogId, take: 20 }),
    ])
    stats.value = statsRes
    reviews.value = listRes.items
    total.value = listRes.total

    if (props.signedIn) {
      try {
        const mine = await request<Review>('/api/reviews/me', { query: { catalog_id: props.catalogId } })
        myReview.value = mine
        rating.value = mine.rating ?? null
        body.value = mine.body ?? ''
      } catch {
        myReview.value = null
      }
    }
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to load reviews'
  } finally {
    loading.value = false
  }
}

async function submitReview() {
  if (!rating.value && !body.value.trim()) {
    error.value = 'Provide a rating or review text.'
    return
  }
  saving.value = true
  error.value = ''
  feedback.value = ''
  try {
    const saved = await request<Review>('/api/reviews', {
      method: 'PUT',
      body: {
        catalog_id: props.catalogId,
        rating: rating.value ?? undefined,
        body: body.value.trim() || undefined,
      },
    })
    myReview.value = saved
    feedback.value = 'Review saved'
    await load()
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to save review'
  } finally {
    saving.value = false
  }
}

async function deleteMyReview() {
  if (!myReview.value) return
  saving.value = true
  error.value = ''
  try {
    await request(`/api/reviews/${myReview.value.id}`, { method: 'DELETE' })
    myReview.value = null
    body.value = ''
    rating.value = 8
    feedback.value = 'Review removed'
    await load()
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to delete review'
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <section class="rounded-2xl bg-base-200 p-5 sm:p-6 space-y-5">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h2 class="text-sm font-mono uppercase tracking-wider text-primary">Reviews &amp; Ratings</h2>
        <p v-if="stats" class="text-xs text-[var(--console-dim)] mt-0.5">
          {{ stats.count }} {{ stats.count === 1 ? 'review' : 'reviews' }}
          <span v-if="stats.average"> · Average <strong class="text-base-content font-mono">{{ stats.average.toFixed(1) }}/10</strong></span>
        </p>
      </div>
    </div>

    <div v-if="error" class="alert alert-error rounded-xl text-sm">{{ error }}</div>
    <div v-if="feedback" class="alert alert-success rounded-xl text-sm">{{ feedback }}</div>

    <form v-if="signedIn" class="rounded-xl bg-base-100 p-4 space-y-3" @submit.prevent="submitReview">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <span class="text-xs font-semibold uppercase tracking-wider text-[var(--console-dim)]">
          {{ myReview ? 'Your Review' : 'Write a Review' }}
        </span>
        <div class="flex items-center gap-2">
          <label class="text-xs text-[var(--console-dim)]">Rating</label>
          <select v-model.number="rating" class="select select-bordered select-xs rounded-lg font-mono">
            <option :value="null">No score</option>
            <option v-for="n in 10" :key="n" :value="n">{{ n }}/10</option>
          </select>
        </div>
      </div>
      <textarea
        v-model="body"
        rows="3"
        placeholder="Share your thoughts on this title..."
        class="textarea textarea-bordered w-full rounded-xl text-sm"
      ></textarea>
      <div class="flex items-center justify-between gap-2">
        <button
          v-if="myReview"
          type="button"
          class="btn btn-ghost btn-xs text-error rounded-lg"
          :disabled="saving"
          @click="deleteMyReview"
        >
          <TrashCan16 class="w-3.5 h-3.5" />
          Delete
        </button>
        <span v-else></span>
        <button type="submit" class="btn btn-primary btn-sm rounded-xl" :disabled="saving">
          {{ saving ? 'Saving...' : myReview ? 'Update Review' : 'Post Review' }}
        </button>
      </div>
    </form>

    <div v-if="loading" class="space-y-2">
      <div v-for="n in 2" :key="n" class="skeleton h-16 rounded-xl"></div>
    </div>
    <p v-else-if="reviews.length === 0" class="text-sm text-[var(--console-faint)]">
      No reviews yet. Be the first to share a rating or review.
    </p>
    <div v-else class="space-y-3">
      <div v-for="r in reviews" :key="r.id" class="rounded-xl bg-base-100 p-4 space-y-2">
        <div class="flex items-center justify-between gap-2">
          <span class="text-xs font-mono text-[var(--console-faint)]">{{ r.account_id.slice(0, 8) }}</span>
          <div class="flex items-center gap-2">
            <span v-if="r.rating" class="badge badge-primary badge-sm font-mono">{{ r.rating }}/10</span>
            <span class="text-[11px] font-mono text-[var(--console-faint)]">{{ formatTimestamp(r.updated_at) }}</span>
          </div>
        </div>
        <p v-if="r.body" class="text-sm leading-relaxed whitespace-pre-line">{{ r.body }}</p>
      </div>
    </div>
  </section>
</template>
