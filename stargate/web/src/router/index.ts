import { createRouter, createWebHistory } from 'vue-router'
import type { RouteMeta } from 'vue-router'

import { i18n } from '@/lib/i18n'
import { useSessionStore } from '@/stores/session'

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    {
      path: '/',
      redirect: '/account',
    },
    {
      path: '/sign-in',
      name: 'sign-in',
      component: () => import('@/views/SignInView.vue'),
      meta: { public: true, titleKey: 'app.titles.signIn' },
    },
    {
      path: '/create-account',
      name: 'create-account',
      component: () => import('@/views/CreateAccountView.vue'),
      meta: { public: true, titleKey: 'app.titles.createAccount' },
    },
    {
      path: '/auth/callback/:provider',
      name: 'auth-callback',
      component: () => import('@/views/CallbackView.vue'),
      meta: { public: true, titleKey: 'app.titles.continuingSignIn' },
    },
    {
      path: '/auth/captcha',
      name: 'captcha',
      component: () => import('@/views/CaptchaView.vue'),
      meta: { public: true, titleKey: 'app.titles.verification' },
    },
    {
      path: '/auth/authorize',
      name: 'authorize',
      component: () => import('@/views/AuthorizeView.vue'),
      meta: { titleKey: 'app.titles.authorizeApp', narrow: true },
    },
    {
      path: '/auth/sudo',
      name: 'sudo',
      component: () => import('@/views/security/SudoView.vue'),
      meta: { titleKey: 'app.titles.sudo', narrow: true },
    },
    {
      path: '/recover',
      name: 'recover',
      component: () => import('@/views/RecoverView.vue'),
      meta: { public: true, titleKey: 'app.titles.resetPassword' },
    },
    {
      path: '/account',
      name: 'account',
      component: () => import('@/views/AccountView.vue'),
      meta: { titleKey: 'app.titles.profile' },
    },
    {
      path: '/security',
      redirect: '/security/sessions',
    },
    {
      path: '/security/sessions',
      name: 'sessions',
      component: () => import('@/views/security/SessionsView.vue'),
      meta: { titleKey: 'app.titles.sessions', wide: true },
    },
    {
      path: '/security/factors',
      name: 'factors',
      component: () => import('@/views/security/FactorsView.vue'),
      meta: { titleKey: 'app.titles.signInFactors' },
    },
    {
      path: '/security/passkeys',
      name: 'passkeys',
      component: () => import('@/views/security/PasskeysView.vue'),
      meta: { titleKey: 'app.titles.passkeys' },
    },
    {
      path: '/security/contacts',
      name: 'contacts',
      component: () => import('@/views/security/ContactsView.vue'),
      meta: { titleKey: 'app.titles.contacts' },
    },
    {
      path: '/security/connections',
      name: 'connections',
      component: () => import('@/views/security/ConnectionsView.vue'),
      meta: { titleKey: 'app.titles.connections' },
    },
    {
      path: '/security/preferences',
      name: 'preferences',
      component: () => import('@/views/security/PreferencesView.vue'),
      meta: { titleKey: 'app.titles.securityMode' },
    },
    {
      path: '/developers',
      name: 'developers',
      component: () => import('@/views/DevelopersView.vue'),
      meta: { titleKey: 'app.titles.developers', wide: true },
    },
    {
      path: '/activity',
      name: 'activity',
      component: () => import('@/views/ActivityView.vue'),
      meta: { titleKey: 'app.titles.activity', wide: true },
    },
    {
      path: '/admin',
      name: 'admin',
      component: () => import('@/views/admin/AdminOverview.vue'),
      meta: { titleKey: 'app.titles.admin', admin: true, full: true, fullBleed: true },
    },
    {
      path: '/admin/accounts',
      name: 'admin-accounts',
      component: () => import('@/views/admin/AdminAccounts.vue'),
      meta: { titleKey: 'app.titles.accounts', admin: true, full: true, fullBleed: true },
    },
    {
      path: '/admin/accounts/:name',
      name: 'admin-account',
      component: () => import('@/views/admin/AdminAccountDetail.vue'),
      meta: { titleKey: 'app.titles.account', admin: true, full: true, fullBleed: true },
    },
    {
      path: '/admin/permissions',
      name: 'admin-permissions',
      component: () => import('@/views/admin/AdminPermissions.vue'),
      meta: { titleKey: 'app.titles.permissions', admin: true, full: true, fullBleed: true },
    },
    {
      path: '/admin/permissions/:id',
      name: 'admin-permission-group',
      component: () => import('@/views/admin/AdminPermissionGroup.vue'),
      meta: { titleKey: 'app.titles.permissionGroup', admin: true, full: true, fullBleed: true },
    },
    {
      path: '/admin/cache',
      name: 'admin-cache',
      component: () => import('@/views/admin/AdminCache.vue'),
      meta: { titleKey: 'app.titles.cache', admin: true, full: true, fullBleed: true },
    },
    {
      path: '/admin/geography',
      name: 'admin-geography',
      component: () => import('@/views/admin/AdminGeography.vue'),
      meta: { titleKey: 'app.titles.geography', admin: true, full: true, fullBleed: true },
    },
    {
      path: '/@:name',
      name: 'public-profile',
      component: () => import('@/views/PublicProfileView.vue'),
      meta: { public: true, profile: true, titleKey: 'app.titles.profile' },
    },
    {
      path: '/:pathMatch(.*)*',
      name: 'not-found',
      component: () => import('@/views/NotFoundView.vue'),
      meta: { public: true, titleKey: 'app.titles.notFound' },
    },
  ],
  scrollBehavior: () => ({ top: 0 }),
})

router.beforeEach(async (to) => {
  const session = useSessionStore()
  if (session.loading) await session.load()

  if (!to.meta.public && !session.signedIn) {
    return { name: 'sign-in', query: { next: to.fullPath } }
  }
  if (
    to.meta.public &&
    session.signedIn &&
    (to.name === 'sign-in' || to.name === 'create-account')
  ) {
    return { name: 'account' }
  }
  if (to.meta.admin && session.account?.is_superuser !== true) {
    return { name: 'account' }
  }
  return true
})

/** Sets the document title from the route's translation key. */
export function applyRouteTitle(meta: RouteMeta) {
  const key = typeof meta.titleKey === 'string' ? meta.titleKey : 'app.console'
  document.title = `${i18n.global.t(key)} · Solarpass`
}

router.afterEach((to) => {
  applyRouteTitle(to.meta)
})

export default router
