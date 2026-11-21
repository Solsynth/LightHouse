<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import RewindViewIsland from '@/islands/RewindViewIsland.vue'

const route = useRoute()
const router = useRouter()
const currentYear = new Date().getFullYear()
const year = computed(() => {
  const parsed = Number(route.params.year)
  return Number.isFinite(parsed) && parsed >= 2000 ? parsed : currentYear
})
const years = [currentYear, currentYear - 1, currentYear - 2]

function selectYear(y: number) {
  router.push(`/rewind/${y}`)
}
</script>

<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h1 class="text-2xl font-bold tracking-tight">{{ year }} Rewind</h1>
        <p class="text-sm text-base-content/65">Your yearly listening, watching, and reading summary</p>
      </div>
      <div class="flex items-center gap-1.5">
        <button
          v-for="y in years"
          :key="y"
          type="button"
          class="chip"
          :class="{ 'chip-active': y === year }"
          @click="selectYear(y)"
        >
          {{ y }}
        </button>
      </div>
    </div>
    <RewindViewIsland :key="year" :year="year" :personal="true" />
  </div>
</template>
