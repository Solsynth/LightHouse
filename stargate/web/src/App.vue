<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Component } from 'vue'
import {
  Activity16,
  Api16,
  Moon16,
  SettingsAdjust16,
  Sun16,
  Tools16,
  User16,
  UserAvatar16,
} from '@carbon/icons-vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'

import { useSessionStore } from '@/stores/session'
import { useNoticeStore } from '@/stores/notice'
import { useUiStore } from '@/stores/ui'
import { applyRouteTitle } from '@/router'
import type { Locale } from '@/lib/prefs'
import ElevationSheet from '@/components/ElevationSheet.vue'

const session = useSessionStore()
const notice = useNoticeStore()
const ui = useUiStore()
const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()

interface NavItem {
  to: string
  labelKey: string
  icon: Component
  scope: 'account' | 'security' | 'admin'
  match: string
}

const nav: NavItem[] = [
  {
    to: '/account',
    labelKey: 'app.nav.profile',
    icon: User16,
    scope: 'account',
    match: '/account',
  },
  {
    to: '/security/sessions',
    labelKey: 'app.nav.settings',
    icon: SettingsAdjust16,
    scope: 'security',
    match: '/security',
  },
  {
    to: '/developers',
    labelKey: 'app.nav.developers',
    icon: Api16,
    scope: 'security',
    match: '/developers',
  },
  {
    to: '/activity',
    labelKey: 'app.nav.activity',
    icon: Activity16,
    scope: 'account',
    match: '/activity',
  },
  { to: '/admin', labelKey: 'app.nav.admin', icon: Tools16, scope: 'admin', match: '/admin' },
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
const backgroundFailed = ref(false)

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

watch(
  () => session.account?.profile?.background?.id,
  () => {
    backgroundFailed.value = false
  },
)

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
    :class="fullBleed ? '' : 'px-4 py-8 sm:py-12'"
  >
    <div
      v-if="session.signedIn && background && !backgroundFailed"
      class="pointer-events-none fixed inset-0 z-0 overflow-hidden"
      aria-hidden="true"
    >
      <img
        :src="backgroundUrl"
        alt=""
        class="h-full w-full scale-110 object-cover opacity-30 blur-3xl"
        @error="backgroundFailed = true"
      />
      <div class="absolute inset-0 bg-base-100/70" />
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
        :class="fullBleed ? 'flex flex-1 flex-col' : 'transition-[max-width] ' + cardWidth"
      >
        <main
          id="main"
          :class="
            fullBleed ? 'flex flex-1 flex-col bg-base-100/40' : 'border border-line bg-base-200/40'
          "
        >
          <div
            class="flex items-center justify-between gap-4 border-b border-line py-3"
            :class="fullBleed ? 'px-5 sm:px-7' : 'px-5'"
          >
            <RouterLink to="/" class="flex items-center gap-2.5" :aria-label="t('app.home')">
              <span class="brand-mark h-[22px] w-[22px] shrink-0" aria-hidden="true" />
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
                class="hidden items-center gap-1.5 text-2xs text-accent sm:flex"
                :title="t('app.elevatedHint')"
              >
                <span class="dot bg-accent" />
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
                    class="h-6 w-6 rounded-[2px] object-cover"
                    @error="avatarFailed = true"
                  />
                  <span
                    v-else
                    class="flex h-6 w-6 items-center justify-center rounded-[2px] text-faint"
                  >
                    <UserAvatar16 aria-hidden="true" />
                  </span>
                </button>
                <ul
                  v-if="menuOpen"
                  class="menu dropdown-content z-50 mt-2 w-44 border border-line bg-base-200 p-1 shadow-none"
                >
                  <li>
                    <RouterLink :to="publicPath" @click="menuOpen = false">
                      {{ t('app.publicProfile') }}
                    </RouterLink>
                  </li>
                  <li>
                    <button type="button" @click="signOutFromMenu">
                      {{ t('app.signOut') }}
                    </button>
                  </li>
                </ul>
              </div>
            </div>
          </div>

          <nav
            v-if="showNav"
            class="flex items-center gap-4 overflow-x-auto border-b border-line py-2"
            :class="fullBleed ? 'px-5 sm:px-7' : 'px-5'"
            :aria-label="t('app.console')"
          >
            <RouterLink
              v-for="item in visibleNav"
              :key="item.to"
              :to="item.to"
              class="flex items-center gap-1.5 whitespace-nowrap text-2xs tracking-[0.04em] transition-colors"
              :class="
                activeSection === item.match ? 'text-base-content' : 'text-faint hover:text-dim'
              "
            >
              <component :is="item.icon" class="shrink-0" aria-hidden="true" />
              {{ t(item.labelKey) }}
            </RouterLink>
          </nav>

          <div ref="region" class="page-region">
            <div ref="regionInner">
              <div :class="edgeToEdge ? '' : 'px-5 py-6 sm:px-7 sm:py-8'">
                <RouterView v-slot="{ Component }">
                  <component :is="Component" />
                </RouterView>
              </div>

              <Transition name="notice">
                <div
                  v-if="latest"
                  class="flex items-baseline gap-2 px-5 py-2 text-2xs"
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
      :class="fullBleed ? 'mt-0 border-t border-line px-5 py-3 sm:px-7' : ['mt-6', cardWidth]"
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
        <select
          class="select select-xs footer-locale"
          :value="ui.locale"
          :aria-label="t('app.language')"
          @change="changeLocale"
        >
          <option value="en">{{ t('app.languageEnglish') }}</option>
          <option value="zh-CN">{{ t('app.languageChinese') }}</option>
        </select>
      </div>
    </footer>

    <ElevationSheet />
  </div>
</template>
