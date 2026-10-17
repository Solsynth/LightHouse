<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import type { Component } from 'vue'
import { Activity16, Api16, Devices16, Tools16, User16 } from '@carbon/icons-vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'

import { useSessionStore } from '@/stores/session'
import { useNoticeStore } from '@/stores/notice'
import ElevationSheet from '@/components/ElevationSheet.vue'

const session = useSessionStore()
const notice = useNoticeStore()
const route = useRoute()
const router = useRouter()

interface NavItem {
  to: string
  label: string
  icon: Component
  scope: 'account' | 'security' | 'admin'
  match: string
}

const nav: NavItem[] = [
  { to: '/account', label: 'Profile', icon: User16, scope: 'account', match: '/account' },
  { to: '/security/sessions', label: 'Sessions', icon: Devices16, scope: 'security', match: '/security' },
  { to: '/developers', label: 'Developers', icon: Api16, scope: 'security', match: '/developers' },
  { to: '/activity', label: 'Activity', icon: Activity16, scope: 'account', match: '/activity' },
  { to: '/admin', label: 'Admin', icon: Tools16, scope: 'admin', match: '/admin' },
]

const showAdmin = computed(() => session.account?.is_superuser === true)

const visibleNav = computed(() => nav.filter((item) => item.scope !== 'admin' || showAdmin.value))

const activeSection = computed(() => {
  const path = route.path
  if (path.startsWith('/admin')) return '/admin'
  if (path.startsWith('/developers')) return '/developers'
  if (path.startsWith('/activity')) return '/activity'
  if (path.startsWith('/security')) return '/security'
  return '/account'
})

// Public flow pages are single-purpose (sign-in, recovery, captcha, provider
// callbacks), so they stay a narrow card even when a session exists. The
// signed-in console needs room.
const cardWidth = computed(() =>
  session.signedIn && !route.meta.public ? 'max-w-[880px]' : 'max-w-[460px]',
)

const showNav = computed(
  () => session.signedIn && !route.path.startsWith('/auth/'),
)

const latest = computed(() => notice.items[notice.items.length - 1])

// The card is the whole page, so a route or notice changing the content should
// move its edge, not snap it. The region is measured from a real height and
// handed back to `auto` once the transition settles.
const region = ref<HTMLElement | null>(null)
const regionInner = ref<HTMLElement | null>(null)
let regionObserver: ResizeObserver | null = null
let regionSettle: number | undefined

onMounted(() => {
  const outer = region.value
  const inner = regionInner.value
  if (!outer || !inner) return

  outer.style.height = `${inner.offsetHeight}px`
  if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
    outer.style.height = 'auto'
    return
  }

  let shown = inner.offsetHeight
  let animating = false
  regionObserver = new ResizeObserver(() => {
    const next = inner.offsetHeight
    if (next === shown && !animating) return
    // ResizeObserver runs after layout, so the rendered box has already
    // adopted the new height. Start from the last settled (or in-flight)
    // height and let the transition carry it to the new one.
    const from = animating ? outer.getBoundingClientRect().height : shown
    // Pin the old height without a transition, then hand the next frame to
    // the CSS transition. Two writes in one callback would be coalesced.
    outer.style.transition = 'none'
    outer.style.height = `${from}px`
    void outer.offsetHeight
    outer.style.transition = ''
    shown = next
    animating = true
    requestAnimationFrame(() => {
      outer.style.height = `${next}px`
    })
    window.clearTimeout(regionSettle)
    regionSettle = window.setTimeout(() => {
      outer.style.height = 'auto'
      animating = false
    }, 260)
  })
  regionObserver.observe(inner)
})

onBeforeUnmount(() => {
  regionObserver?.disconnect()
  window.clearTimeout(regionSettle)
})

async function signOut() {
  await session.signOut()
  notice.ok('Session ended. Cookies cleared.')
  await router.push('/sign-in')
}
</script>

<template>
  <div class="flex min-h-dvh flex-col items-center justify-center px-4 py-8 sm:py-12">
    <a
      href="#main"
      class="sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-4 focus:z-50 focus:bg-base-300 focus:px-3 focus:py-2 focus:text-xs"
    >
      Skip to content
    </a>

    <div class="w-full transition-[max-width]" :class="cardWidth">
      <main id="main" class="border border-line bg-base-200/40">
        <div class="flex items-center justify-between gap-4 border-b border-line px-5 py-3">
          <RouterLink to="/" class="flex items-center gap-2.5" aria-label="Solarpass home">
            <img
              src="/icon-outline.svg"
              alt=""
              class="h-[22px] w-[22px] shrink-0"
              aria-hidden="true"
            />
            <span class="text-[13px] font-semibold tracking-[0.24em] text-base-content">
              SOLARPASS
            </span>
            <span class="hidden text-2xs tracking-[0.12em] text-faint sm:inline">
              Solar Network
            </span>
          </RouterLink>

          <div v-if="session.signedIn" class="flex items-center gap-3">
            <span
              v-if="session.elevated"
              class="hidden items-center gap-1.5 text-2xs text-accent sm:flex"
              title="This session can perform elevated actions"
            >
              <span class="dot bg-accent" />
              elevated
            </span>
            <span class="text-2xs text-dim">{{ session.identity?.name }}</span>
            <button class="btn btn-ghost btn-xs" type="button" @click="signOut">Sign Out</button>
          </div>
        </div>

        <nav
          v-if="showNav"
          class="flex items-center gap-4 overflow-x-auto border-b border-line px-5 py-2"
          aria-label="Console"
        >
          <RouterLink
            v-for="item in visibleNav"
            :key="item.to"
            :to="item.to"
            class="flex items-center gap-1.5 whitespace-nowrap text-2xs tracking-[0.04em] transition-colors"
            :class="
              activeSection === item.match
                ? 'text-base-content'
                : 'text-faint hover:text-dim'
            "
          >
            <component :is="item.icon" class="shrink-0" aria-hidden="true" />
            {{ item.label }}
          </RouterLink>
        </nav>

        <div ref="region" class="page-region">
          <div ref="regionInner">
            <div class="px-5 py-6 sm:px-7 sm:py-8">
              <RouterView v-slot="{ Component }">
                <component :is="Component" />
              </RouterView>
            </div>

            <Transition name="notice">
              <div
                v-if="latest"
                class="flex items-baseline gap-2 border-t border-line px-5 py-2 text-2xs"
                role="status"
                aria-live="polite"
              >
                <span :class="latest.tone === 'error' ? 'text-error' : 'text-success'">
                  {{ latest.tone === 'error' ? 'err' : 'ok' }}
                </span>
                <span class="truncate text-dim">{{ latest.message }}</span>
              </div>
            </Transition>
          </div>
        </div>
      </main>
    </div>

    <ElevationSheet />
  </div>
</template>
