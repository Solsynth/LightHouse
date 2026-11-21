<script setup lang="ts">
import { computed, onMounted, provide, ref } from 'vue'
import { RouterLink, RouterView, useRoute } from 'vue-router'
import { Asleep16, Light16, Login16 } from '@carbon/icons-vue'
import { request } from '@/lib/api'

interface MeUser {
  id: string
  name: string
  nick: string
  avatar_url?: string
}

interface MeResponse {
  authenticated: boolean
  sign_in_url: string
  user?: MeUser | null
}

const route = useRoute()
const me = ref<MeResponse | null>(null)
const authLoading = ref(true)
const theme = ref<'stargate' | 'stargate-light'>('stargate')

const isAuthenticated = computed(() => Boolean(me.value?.authenticated && me.value?.user))
provide('pulsarAuth', { me, isAuthenticated })

const navItems = [
  { to: '/', label: 'Overview', name: 'overview' },
  { to: '/history', label: 'History', name: 'history' },
  { to: '/collections', label: 'Collections', name: 'collections' },
  { to: '/catalog', label: 'Catalog', name: 'catalog' },
  { to: '/activities', label: 'Activities', name: 'activities' },
  { to: '/rewind', label: 'Rewind', name: 'rewind' },
  { to: '/settings', label: 'Settings', name: 'settings' },
]

function applyTheme(next: 'stargate' | 'stargate-light') {
  theme.value = next
  document.documentElement.setAttribute('data-theme', next)
  try {
    localStorage.setItem('pulsar-theme', next)
  } catch {
    // ignore storage errors
  }
}

function toggleTheme() {
  applyTheme(theme.value === 'stargate' ? 'stargate-light' : 'stargate')
}

onMounted(async () => {
  try {
    const saved = localStorage.getItem('pulsar-theme')
    if (saved === 'stargate-light' || saved === 'stargate') {
      applyTheme(saved)
    }
  } catch {
    // ignore
  }
  try {
    me.value = await request<MeResponse>('/api/me')
  } catch {
    me.value = { authenticated: false, sign_in_url: '/sign-in', user: null }
  } finally {
    authLoading.value = false
  }
})
</script>

<template>
  <div class="min-h-screen flex flex-col bg-base-100 text-base-content">
    <header class="sticky top-0 z-30 border-b border-base-300/60 bg-base-100/90 backdrop-blur-md">
      <div class="mx-auto flex max-w-6xl items-center justify-between gap-4 px-4 py-3">
        <div class="flex items-center gap-6 min-w-0">
          <RouterLink to="/" class="flex items-center gap-2 shrink-0">
            <span class="grid h-7 w-7 place-items-center rounded-lg bg-primary text-primary-content text-xs font-extrabold">
              P
            </span>
            <span class="font-bold tracking-tight text-base">Pulsar</span>
            <span class="badge badge-xs badge-ghost uppercase">Manage</span>
          </RouterLink>

          <nav class="hidden md:flex items-center gap-1 overflow-x-auto">
            <RouterLink
              v-for="item in navItems"
              :key="item.to"
              :to="item.to"
              class="px-3 py-1.5 rounded-lg text-sm font-medium transition-colors whitespace-nowrap"
              :class="
                route.name === item.name ||
                (item.name === 'collections' && route.name === 'collection-detail') ||
                (item.name === 'catalog' && route.name === 'catalog-item')
                  ? 'bg-primary/15 text-primary'
                  : 'text-base-content/70 hover:text-base-content hover:bg-base-200/70'
              "
            >
              {{ item.label }}
            </RouterLink>
          </nav>
        </div>

        <div class="flex items-center gap-2 shrink-0">
          <button
            type="button"
            class="btn btn-ghost btn-sm btn-square"
            :aria-label="theme === 'stargate' ? 'Switch to light theme' : 'Switch to dark theme'"
            @click="toggleTheme"
          >
            <Light16 v-if="theme === 'stargate'" />
            <Asleep16 v-else />
          </button>

          <div v-if="isAuthenticated && me?.user" class="flex items-center gap-2 rounded-full bg-base-200/80 pl-1.5 pr-3 py-1">
            <img
              v-if="me.user.avatar_url"
              :src="me.user.avatar_url"
              :alt="me.user.nick || me.user.name"
              class="h-6 w-6 rounded-full object-cover"
            />
            <span
              v-else
              class="grid h-6 w-6 place-items-center rounded-full bg-primary/20 text-primary text-xs font-bold"
            >
              {{ (me.user.nick || me.user.name || '?').slice(0, 1).toUpperCase() }}
            </span>
            <span class="text-xs font-semibold truncate max-w-28">
              {{ me.user.nick || me.user.name }}
            </span>
          </div>

          <a
            v-else-if="!authLoading"
            :href="me?.sign_in_url || '/sign-in'"
            class="btn btn-primary btn-sm gap-1.5"
          >
            <Login16 />
            <span>Sign in</span>
          </a>
        </div>
      </div>

      <!-- Mobile nav -->
      <nav class="flex md:hidden items-center gap-1 overflow-x-auto px-4 pb-2.5 border-t border-base-300/40 pt-2">
        <RouterLink
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="px-2.5 py-1 rounded-lg text-xs font-medium whitespace-nowrap"
          :class="route.name === item.name ? 'bg-primary/15 text-primary' : 'text-base-content/70'"
        >
          {{ item.label }}
        </RouterLink>
      </nav>
    </header>

    <main class="mx-auto w-full max-w-6xl flex-1 px-4 py-6">
      <div v-if="authLoading" class="flex items-center justify-center py-20 text-sm text-base-content/60">
        Loading Pulsar workspace...
      </div>

      <div
        v-else-if="!isAuthenticated && route.meta?.requiresAuth"
        class="mx-auto max-w-md rounded-2xl border border-base-300/60 bg-base-200/60 p-8 text-center space-y-4 mt-8"
      >
        <div class="mx-auto grid h-12 w-12 place-items-center rounded-2xl bg-primary/15 text-primary font-extrabold text-lg">
          P
        </div>
        <h1 class="text-xl font-bold">Sign in to manage your Pulsar profile</h1>
        <p class="text-sm text-base-content/70">
          Manage your now-playing session, scrobble history, collections, shelves, reviews, and connected services.
        </p>
        <div class="flex items-center justify-center gap-3 pt-2">
          <a :href="me?.sign_in_url || '/sign-in'" class="btn btn-primary gap-2">
            <Login16 />
            Sign in with Solar
          </a>
          <RouterLink to="/catalog" class="btn btn-ghost">
            Browse Catalog
          </RouterLink>
        </div>
      </div>

      <RouterView v-else />
    </main>

    <footer class="border-t border-base-300/50 py-4 text-xs text-base-content/50">
      <div class="mx-auto flex max-w-6xl items-center justify-between px-4">
        <span>Pulsar · Solar Network</span>
        <span>Profile &amp; Library Management</span>
      </div>
    </footer>
  </div>
</template>
