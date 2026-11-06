<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Component } from 'vue'
import {
  Activity16,
  Friendship16,
  Moon16,
  SettingsAdjust16,
  Sun16,
  Tools16,
  User16,
  UserAvatar16,
} from '@carbon/icons-vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'

import { MODULES } from '@/lib/modules'
import { useModulesStore } from '@/stores/modules'
import { useNoticeStore } from '@/stores/notice'
import { useSessionStore } from '@/stores/session'
import { useUiStore } from '@/stores/ui'
import { applyRouteTitle } from '@/router'
import type { Locale } from '@/lib/prefs'
import ElevationSheet from '@/components/ElevationSheet.vue'
import PendingLoginPrompt from '@/components/PendingLoginPrompt.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import TextField from '@/components/TextField.vue'

const session = useSessionStore()
const notice = useNoticeStore()
const ui = useUiStore()
const modules = useModulesStore()
const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()

interface NavItem {
  to: string
  labelKey: string
  icon: Component
  scope: 'account' | 'security' | 'admin'
  match: string
  /** Module IDs that must be mounted for this entry's routes to exist. */
  modules?: string[]
}

const nav: NavItem[] = [
  {
    to: '/account',
    labelKey: 'app.nav.profile',
    icon: User16,
    scope: 'account',
    match: '/account',
    modules: [MODULES.profiles],
  },
  {
    to: '/account/relationships',
    labelKey: 'app.nav.friends',
    icon: Friendship16,
    scope: 'account',
    match: '/account/relationships',
    modules: [MODULES.relationships],
  },
  {
    to: '/security/sessions',
    labelKey: 'app.nav.settings',
    icon: SettingsAdjust16,
    scope: 'security',
    match: '/security',
    modules: [MODULES.accounts],
  },
  {
    to: '/activity',
    labelKey: 'app.nav.activity',
    icon: Activity16,
    scope: 'account',
    match: '/activity',
    modules: [MODULES.accounts],
  },
  {
    to: '/admin',
    labelKey: 'app.nav.admin',
    icon: Tools16,
    scope: 'admin',
    match: '/admin',
    modules: [MODULES.admin],
  },
]

const showAdmin = computed(() => session.account?.is_superuser === true)

// A nav entry disappears when the module serving its routes is not mounted:
// the router redirects those routes anyway, and a tab that always lands on the
// fallback is worse than no tab.
const visibleNav = computed(() =>
  nav.filter(
    (item) => (item.scope !== 'admin' || showAdmin.value) && modules.hasAll(item.modules ?? []),
  ),
)

const activeSection = computed(() => {
  const path = route.path
  if (path.startsWith('/account/relationships')) return '/account/relationships'
  if (path.startsWith('/admin')) return '/admin'
  if (path.startsWith('/activity')) return '/activity'
  if (path.startsWith('/security')) return '/security'
  return '/account'
})

// Public flow pages are single-purpose (sign-in, recovery, captcha, provider
// callbacks), so they stay a narrow card even when a session exists. Data
// surfaces get more room than forms: ledgers and admin pages would waste
// horizontal space at the account card's width.
const cardWidth = computed(() => {
  if (route.meta.narrow) return 'max-w-[460px]'
  if (route.meta.full) return 'max-w-none'
  if (route.meta.wide) return 'max-w-[1120px]'
  if (route.meta.profile) return 'max-w-[880px]'
  if (route.meta.public || !session.signedIn) return 'max-w-[460px]'
  return 'max-w-[880px]'
})

const showNav = computed(
  () => session.signedIn && !route.path.startsWith('/auth/') && route.name !== 'public-profile',
)

// Vue Router renders nothing until its first navigation resolves, and that
// navigation waits for the session and module reads. Without a placeholder the
// shell shows an empty card for that whole window, which reads as a broken
// page on a slow connection. `matched` is empty only on the initial location.
const booting = computed(() => route.matched.length === 0)

const latest = computed(() => notice.items[notice.items.length - 1])

const picture = computed(() => session.account?.profile?.picture)
const pictureUrl = computed(
  () =>
    session.account?.profile?.picture?.url ||
    `/api/accounts/${encodeURIComponent(session.account?.name ?? session.identity?.name ?? '')}/picture`,
)
const background = computed(() => session.account?.profile?.background)
const backgroundUrl = computed(
  () =>
    background.value?.url ||
    `/api/accounts/${encodeURIComponent(session.account?.name ?? '')}/background`,
)
const publicPath = computed(() => `/@${encodeURIComponent(session.account?.name ?? '')}`)
const avatarFailed = ref(false)

// The consent screen lends the requesting app's own background to the shell,
// so that page is painted by the app being authorized rather than by the
// account authorizing it. Any other page falls back to the account backdrop.
const backdropUrl = computed(() => ui.appBackdrop || (background.value ? backgroundUrl.value : ''))
const backdropFailed = ref(false)

const menuOpen = ref(false)
const menuRoot = ref<HTMLElement | null>(null)
const fullBleed = computed(() => Boolean(route.meta.fullBleed))
const edgeToEdge = computed(() => Boolean(route.meta.edgeToEdge))

function closeMenuOutside(event: MouseEvent) {
  if (!menuRoot.value?.contains(event.target as Node)) menuOpen.value = false
}

function signOutFromMenu() {
  menuOpen.value = false
  signOut()
}

onMounted(() => document.addEventListener('click', closeMenuOutside))
onBeforeUnmount(() => document.removeEventListener('click', closeMenuOutside))
watch(
  () => route.fullPath,
  () => {
    menuOpen.value = false
  },
)

watch(
  () => session.account?.profile?.picture?.id,
  () => {
    avatarFailed.value = false
  },
)

// A page that lends a backdrop (the consent screen) can swap it while the
// shell stays mounted, so the failure latch resets with the URL, not just with
// the account's own background.
watch(backdropUrl, () => {
  backdropFailed.value = false
})

// The card is the whole page, so a route or notice changing the content should
// move its edge, not snap it. The region is measured from a real height and
// handed back to `auto` once the transition settles.
const region = ref<HTMLElement | null>(null)
const regionInner = ref<HTMLElement | null>(null)
let regionObserver: ResizeObserver | null = null
let regionSettle: number | undefined

// The boot placeholder occupies the region until the first navigation
// resolves, so the measured element appears after mount; the observer attaches
// when it does.
watch(
  regionInner,
  (inner) => {
    const outer = region.value
    if (!inner || !outer) return

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
  },
  { flush: 'post' },
)

onBeforeUnmount(() => {
  regionObserver?.disconnect()
  window.clearTimeout(regionSettle)
})

async function signOut() {
  await session.signOut()
  notice.ok(t('app.sessionEnded'))
  await router.push('/sign-in')
}

// Route changes set the title; a language switch must rewrite it for the
// page that is already open.
watch(locale, () => applyRouteTitle(route.meta))

const year = new Date().getFullYear()

function changeLocale(event: Event) {
  ui.setLocale((event.target as HTMLSelectElement).value as Locale)
}
</script>

<template>
  <div
    class="relative flex min-h-dvh flex-col items-center"
    :class="fullBleed ? '' : 'sm:px-page sm:py-14'"
  >
    <div
      v-if="session.signedIn && backdropUrl && !backdropFailed"
      class="pointer-events-none fixed inset-0 z-0 overflow-hidden"
      aria-hidden="true"
    >
      <img
        :src="backdropUrl"
        alt=""
        class="h-full w-full scale-110 object-cover opacity-40 blur-2xl"
        @error="backdropFailed = true"
      />
      <div class="absolute inset-0 bg-base-100/55" />
    </div>

    <a
      href="#main"
      class="sr-only focus:not-sr-only focus:absolute focus:left-4 focus:top-4 focus:z-50 focus:bg-base-300 focus:px-3 focus:py-2 focus:text-xs"
    >
      {{ t('app.skipToContent') }}
    </a>

    <div
      class="relative z-10 flex w-full flex-1 flex-col items-center"
      :class="fullBleed ? 'justify-start' : 'justify-center'"
    >
      <div
        class="w-full"
        :class="
          fullBleed
            ? 'flex flex-1 flex-col'
            : 'max-sm:flex max-sm:max-w-none max-sm:flex-1 max-sm:flex-col transition-[max-width] ' +
              cardWidth
        "
      >
        <main
          id="main"
          :class="
            fullBleed
              ? 'flex flex-1 flex-col bg-base-100/40 max-sm:pt-safe'
              : 'max-sm:flex max-sm:flex-1 max-sm:flex-col overflow-hidden rounded-sheet bg-base-200/50 shadow-[0_24px_60px_-40px_rgb(0_0_0/0.55)] max-sm:rounded-none max-sm:shadow-none max-sm:pt-safe'
          "
        >
          <div
            class="flex items-center justify-between gap-4 py-3.5"
            :class="fullBleed ? 'px-shell sm:px-shell-wide' : 'px-shell'"
          >
            <RouterLink to="/" class="group flex items-center gap-2.5" :aria-label="t('app.home')">
              <span
                class="brand-mark brand-mark-live h-[22px] w-[22px] shrink-0"
                aria-hidden="true"
              />
              <span class="text-[13px] font-semibold tracking-[0.14em] text-base-content">
                SOLARPASS
              </span>
              <span class="hidden text-2xs tracking-[0.04em] text-faint sm:inline">
                {{ t('app.network') }}
              </span>
            </RouterLink>

            <div v-if="session.signedIn" class="flex items-center gap-3">
              <span
                v-if="session.elevated"
                class="hidden items-center gap-1.5 text-2xs text-primary sm:flex"
                :title="t('app.elevatedHint')"
              >
                <span class="dot bg-primary" />
                {{ t('app.elevated') }}
              </span>
              <div ref="menuRoot" class="dropdown dropdown-end" @keydown.esc="menuOpen = false">
                <button
                  type="button"
                  class="block shrink-0"
                  :title="session.account?.name"
                  :aria-label="session.account?.name"
                  aria-haspopup="menu"
                  :aria-expanded="menuOpen"
                  @click="menuOpen = !menuOpen"
                >
                  <img
                    v-if="picture && !avatarFailed"
                    :src="pictureUrl"
                    alt=""
                    class="h-6 w-6 rounded-full object-cover"
                    @error="avatarFailed = true"
                  />
                  <span
                    v-else
                    class="flex h-6 w-6 items-center justify-center rounded-full bg-base-300/60 text-faint"
                  >
                    <UserAvatar16 aria-hidden="true" />
                  </span>
                </button>
                <ul
                  v-if="menuOpen"
                  class="menu-plate dropdown-content z-50 mt-2 w-48 p-1.5 text-xs"
                >
                  <li>
                    <RouterLink :to="publicPath" class="post-menu-row" @click="menuOpen = false">
                      {{ t('app.publicProfile') }}
                    </RouterLink>
                  </li>
                  <li class="menu-sep" />
                  <li>
                    <button
                      type="button"
                      class="post-menu-row hover:!bg-error/10 hover:!text-error"
                      @click="signOutFromMenu"
                    >
                      {{ t('app.signOut') }}
                    </button>
                  </li>
                </ul>
              </div>
            </div>
          </div>

          <nav
            v-if="showNav"
            class="flex items-center gap-1.5 overflow-x-auto px-shell py-2 scrollbar-none sm:px-shell-wide"
            :aria-label="t('app.console')"
          >
            <RouterLink
              v-for="item in visibleNav"
              :key="item.to"
              :to="item.to"
              class="nav-tab"
              :class="activeSection === item.match ? 'nav-tab-active' : ''"
            >
              <component :is="item.icon" class="shrink-0" aria-hidden="true" />
              {{ t(item.labelKey) }}
            </RouterLink>
          </nav>

          <div ref="region" class="page-region">
            <div v-if="booting" class="px-shell py-7 sm:px-shell-wide sm:py-9">
              <div class="mb-8 space-y-3" aria-hidden="true">
                <span class="skeleton h-6 w-40" />
                <span class="skeleton h-2.5 w-64 max-w-full" />
              </div>
              <section
                class="overflow-hidden rounded-card bg-base-200/50"
                aria-hidden="true"
              >
                <div class="px-6 pb-1.5 pt-5">
                  <span class="skeleton h-2 w-24" />
                </div>
                <div class="px-6 py-5">
                  <LoadingSkeleton :rows="4" :columns="[3, 2]" header />
                </div>
              </section>
              <p class="sr-only" role="status">{{ t('common.loading') }}</p>
            </div>
            <div v-else ref="regionInner">
              <div :class="edgeToEdge ? '' : 'px-shell py-7 sm:px-shell-wide sm:py-9'">
                <RouterView v-slot="{ Component }">
                  <component :is="Component" />
                </RouterView>
              </div>

              <Transition name="notice">
                <div
                  v-if="latest"
                  class="flex items-baseline gap-2 px-shell py-2.5 text-2xs"
                  role="status"
                  aria-live="polite"
                >
                  <span :class="latest.tone === 'error' ? 'text-error' : 'text-success'">
                    {{ latest.tone === 'error' ? 'ERROR' : 'OK' }}
                  </span>
                  <span class="truncate text-dim">{{ latest.message }}</span>
                </div>
              </Transition>
            </div>
          </div>
        </main>
      </div>
    </div>

    <footer
      class="relative z-10 flex w-full flex-wrap items-center justify-between gap-x-4 gap-y-2 text-2xs text-faint"
      :class="
        fullBleed
          ? 'mt-0 rounded-card bg-base-200/40 px-shell py-3.5 sm:px-shell-wide max-sm:rounded-none max-sm:pb-safe'
          : [
              'mt-8 max-sm:mt-0 max-sm:max-w-none max-sm:rounded-none max-sm:bg-base-200/50 max-sm:px-shell max-sm:py-3.5 max-sm:pb-safe',
              cardWidth,
            ]
      "
    >
      <span>{{ t('app.copyright', { year }) }}</span>
      <div class="flex items-center gap-3">
        <button
          type="button"
          class="btn btn-ghost btn-xs px-1.5"
          :title="ui.isDark ? t('app.switchToLight') : t('app.switchToDark')"
          :aria-label="ui.isDark ? t('app.switchToLight') : t('app.switchToDark')"
          @click="ui.toggleTheme()"
        >
          <component :is="ui.isDark ? Sun16 : Moon16" aria-hidden="true" />
        </button>
        <TextField
          size="xs"
          bare
          root-class="footer-locale"
          :model-value="ui.locale"
          as="select"
          :aria-label="t('app.language')"
          @change="changeLocale"
        >
          <option value="en">{{ t('app.languageEnglish') }}</option>
          <option value="zh-CN">{{ t('app.languageChinese') }}</option>
        </TextField>
      </div>
    </footer>

    <ElevationSheet />
    <PendingLoginPrompt />
  </div>
</template>
