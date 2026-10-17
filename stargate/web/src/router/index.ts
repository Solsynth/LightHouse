import { createRouter, createWebHistory } from 'vue-router'

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
      meta: { public: true, title: 'Sign In' },
    },
    {
      path: '/create-account',
      name: 'create-account',
      component: () => import('@/views/CreateAccountView.vue'),
      meta: { public: true, title: 'Create Account' },
    },
    {
      path: '/auth/callback/:provider',
      name: 'auth-callback',
      component: () => import('@/views/CallbackView.vue'),
      meta: { public: true, title: 'Continuing Sign-In' },
    },
    {
      path: '/auth/captcha',
      name: 'captcha',
      component: () => import('@/views/CaptchaView.vue'),
      meta: { public: true, title: 'Verification' },
    },
    {
      path: '/auth/authorize',
      name: 'authorize',
      component: () => import('@/views/AuthorizeView.vue'),
      meta: { title: 'Authorize App' },
    },
    {
      path: '/recover',
      name: 'recover',
      component: () => import('@/views/RecoverView.vue'),
      meta: { public: true, title: 'Reset Password' },
    },
    {
      path: '/account',
      name: 'account',
      component: () => import('@/views/AccountView.vue'),
      meta: { title: 'Profile' },
    },
    {
      path: '/security',
      redirect: '/security/sessions',
    },
    {
      path: '/security/sessions',
      name: 'sessions',
      component: () => import('@/views/security/SessionsView.vue'),
      meta: { title: 'Sessions' },
    },
    {
      path: '/security/factors',
      name: 'factors',
      component: () => import('@/views/security/FactorsView.vue'),
      meta: { title: 'Sign-In Factors' },
    },
    {
      path: '/security/contacts',
      name: 'contacts',
      component: () => import('@/views/security/ContactsView.vue'),
      meta: { title: 'Contacts' },
    },
    {
      path: '/security/preferences',
      name: 'preferences',
      component: () => import('@/views/security/PreferencesView.vue'),
      meta: { title: 'Security Mode' },
    },
    {
      path: '/developers',
      name: 'developers',
      component: () => import('@/views/DevelopersView.vue'),
      meta: { title: 'Developers' },
    },
    {
      path: '/activity',
      name: 'activity',
      component: () => import('@/views/ActivityView.vue'),
      meta: { title: 'Activity' },
    },
    {
      path: '/admin',
      name: 'admin',
      component: () => import('@/views/admin/AdminOverview.vue'),
      meta: { title: 'Admin', admin: true },
    },
    {
      path: '/admin/accounts',
      name: 'admin-accounts',
      component: () => import('@/views/admin/AdminAccounts.vue'),
      meta: { title: 'Accounts', admin: true },
    },
    {
      path: '/admin/accounts/:name',
      name: 'admin-account',
      component: () => import('@/views/admin/AdminAccountDetail.vue'),
      meta: { title: 'Account', admin: true },
    },
    {
      path: '/admin/permissions',
      name: 'admin-permissions',
      component: () => import('@/views/admin/AdminPermissions.vue'),
      meta: { title: 'Permissions', admin: true },
    },
    {
      path: '/admin/permissions/:id',
      name: 'admin-permission-group',
      component: () => import('@/views/admin/AdminPermissionGroup.vue'),
      meta: { title: 'Permission Group', admin: true },
    },
    {
      path: '/admin/cache',
      name: 'admin-cache',
      component: () => import('@/views/admin/AdminCache.vue'),
      meta: { title: 'Cache', admin: true },
    },
    {
      path: '/admin/geography',
      name: 'admin-geography',
      component: () => import('@/views/admin/AdminGeography.vue'),
      meta: { title: 'Geography', admin: true },
    },
    {
      path: '/:pathMatch(.*)*',
      name: 'not-found',
      component: () => import('@/views/NotFoundView.vue'),
      meta: { public: true, title: 'Not Found' },
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

router.afterEach((to) => {
  const title = typeof to.meta.title === 'string' ? to.meta.title : 'Console'
  document.title = `${title} · Solarpass`
})

export default router
