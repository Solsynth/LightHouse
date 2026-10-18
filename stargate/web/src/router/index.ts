import { createRouter, createWebHistory } from 'vue-router'
import type { LocationQuery, RouteMeta } from 'vue-router'

import { MODULES } from '@/lib/modules'
import { i18n } from '@/lib/i18n'
import { useModulesStore } from '@/stores/modules'
import { useSessionStore } from '@/stores/session'

declare module 'vue-router' {
  interface RouteMeta {
    /**
     * Module IDs that must be enabled for this route to be served. A route
     * with an unmounted module is redirected by the global guard.
     */
    modules?: string[]
  }
}

/**
 * The token pair a social sign-in appends to its return URL, for clients that
 * cannot read the HttpOnly cookies the same redirect sets. This console reads
 * the cookie and never stores a token, so these parameters are erased from the
 * address the moment the app sees them.
 */
const SESSION_TOKEN_PARAMS = ['token', 'refreshToken', 'expiresIn', 'refreshExpiresIn'] as const

function withoutTokenParams(query: LocationQuery): LocationQuery {
  const rest: LocationQuery = { ...query }
  for (const key of SESSION_TOKEN_PARAMS) delete rest[key]
  return rest
}
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
      // The landing page Stargate redirects to when a social round-trip has no
      // explicit return URL: it picks the session up and drops the tokens.
      path: '/auth/success',
      name: 'auth-landing',
      component: () => import('@/views/AuthLandingView.vue'),
      meta: { public: true, titleKey: 'app.titles.continuingSignIn', narrow: true },
    },
    {
      path: '/auth/captcha',
      name: 'captcha',
      component: () => import('@/views/CaptchaView.vue'),
      meta: { public: true, titleKey: 'app.titles.verification' },
    },
    {
      // The consent screen is a two-column request — the app on one side, its
      // scopes and the decision on the other — so it takes the account card
      // rather than the single-column flow width.
      path: '/auth/authorize',
      name: 'authorize',
      component: () => import('@/views/AuthorizeView.vue'),
      meta: { titleKey: 'app.titles.authorizeApp' },
    },
    {
      // The device flow's verification URI: Stargate mints
      // `{siteUrl}/auth/device?code=XXXX-XXXX` and ships it in the device-code
      // response, so this is the path a person is told to open. Approving
      // needs a session; the user code is what identifies the request.
      path: '/auth/device',
      name: 'device',
      component: () => import('@/views/DeviceView.vue'),
      meta: { titleKey: 'app.titles.device' },
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
      // Where the emailed magic-spell links land. Stargate mints
      // `{siteUrl}/spells/{word}` for verification and password-reset emails,
      // so this must stay a public route with the word in the path.
      path: '/spells/:word',
      name: 'spell',
      component: () => import('@/views/SpellView.vue'),
      meta: { public: true, titleKey: 'app.titles.spell', narrow: true },
    },
    {
      path: '/account',
      name: 'account',
      component: () => import('@/views/AccountView.vue'),
      meta: { titleKey: 'app.titles.profile', modules: [MODULES.profiles] },
    },
    {
      // The social graph. The path matches the action URI Stargate attaches to
      // friend-request pushes and the deep link the fleet's app links declare,
      // so a notification opens this page rather than the not-found route.
      path: '/account/relationships',
      name: 'relationships',
      component: () => import('@/views/RelationshipsView.vue'),
      meta: { titleKey: 'app.titles.relationships', wide: true, modules: [MODULES.relationships] },
    },
    {
      path: '/security',
      redirect: '/security/sessions',
    },
    {
      path: '/security/sessions',
      name: 'sessions',
      component: () => import('@/views/security/SessionsView.vue'),
      meta: { titleKey: 'app.titles.sessions', wide: true, modules: [MODULES.accounts] },
    },
    {
      // Devices are the auth clients a session binds to: one row per browser
      // or app install, with the sessions it authorized. Revoking a device
      // expires every session on it, which is why it sits beside Sessions.
      path: '/security/devices',
      name: 'devices',
      component: () => import('@/views/security/DevicesView.vue'),
      meta: { titleKey: 'app.titles.devices', wide: true, modules: [MODULES.accounts] },
    },
    {
      path: '/security/factors',
      name: 'factors',
      component: () => import('@/views/security/FactorsView.vue'),
      meta: { titleKey: 'app.titles.signInFactors', modules: [MODULES.accounts] },
    },
    {
      path: '/security/passkeys',
      name: 'passkeys',
      component: () => import('@/views/security/PasskeysView.vue'),
      meta: { titleKey: 'app.titles.passkeys', modules: [MODULES.accounts] },
    },
    {
      path: '/security/contacts',
      name: 'contacts',
      component: () => import('@/views/security/ContactsView.vue'),
      meta: { titleKey: 'app.titles.contacts', modules: [MODULES.accounts] },
    },
    {
      path: '/security/connections',
      name: 'connections',
      component: () => import('@/views/security/ConnectionsView.vue'),
      meta: { titleKey: 'app.titles.connections', modules: [MODULES.accounts] },
    },
    {
      path: '/security/preferences',
      name: 'preferences',
      component: () => import('@/views/security/PreferencesView.vue'),
      meta: { titleKey: 'app.titles.securityMode', modules: [MODULES.accounts] },
    },
    {
      path: '/developers',
      name: 'developers',
      component: () => import('@/views/DevelopersView.vue'),
      meta: { titleKey: 'app.titles.developers', wide: true, modules: [MODULES.accounts] },
    },
    {
      // One registration: its identity, OAuth configuration, and client
      // secrets. `appId` accepts the id or the slug on the server, but links
      // carry the id so a rename never strands an open tab.
      path: '/developers/apps/:appId',
      name: 'oauth-app',
      component: () => import('@/views/OAuthAppView.vue'),
      meta: {
        titleKey: 'app.titles.oauthApp',
        wide: true,
        modules: [MODULES.accounts, MODULES.oauthapps],
      },
    },
    {
      path: '/activity',
      name: 'activity',
      component: () => import('@/views/ActivityView.vue'),
      meta: { titleKey: 'app.titles.activity', wide: true, modules: [MODULES.accounts] },
    },
    {
      path: '/admin',
      name: 'admin',
      component: () => import('@/views/admin/AdminOverview.vue'),
      meta: {
        titleKey: 'app.titles.admin',
        admin: true,
        full: true,
        fullBleed: true,
        modules: [MODULES.admin],
      },
    },
    {
      path: '/admin/accounts',
      name: 'admin-accounts',
      component: () => import('@/views/admin/AdminAccounts.vue'),
      meta: {
        titleKey: 'app.titles.accounts',
        admin: true,
        full: true,
        fullBleed: true,
        modules: [MODULES.admin],
      },
    },
    {
      path: '/admin/accounts/:name',
      name: 'admin-account',
      component: () => import('@/views/admin/AdminAccountDetail.vue'),
      meta: {
        titleKey: 'app.titles.account',
        admin: true,
        full: true,
        fullBleed: true,
        modules: [MODULES.admin],
      },
    },
    {
      path: '/admin/permissions',
      name: 'admin-permissions',
      component: () => import('@/views/admin/AdminPermissions.vue'),
      meta: {
        titleKey: 'app.titles.permissions',
        admin: true,
        full: true,
        fullBleed: true,
        modules: [MODULES.admin],
      },
    },
    {
      path: '/admin/permissions/:id',
      name: 'admin-permission-group',
      component: () => import('@/views/admin/AdminPermissionGroup.vue'),
      meta: {
        titleKey: 'app.titles.permissionGroup',
        admin: true,
        full: true,
        fullBleed: true,
        modules: [MODULES.admin],
      },
    },
    {
      path: '/admin/cache',
      name: 'admin-cache',
      component: () => import('@/views/admin/AdminCache.vue'),
      meta: {
        titleKey: 'app.titles.cache',
        admin: true,
        full: true,
        fullBleed: true,
        modules: [MODULES.admin],
      },
    },
    {
      path: '/admin/geography',
      name: 'admin-geography',
      component: () => import('@/views/admin/AdminGeography.vue'),
      meta: {
        titleKey: 'app.titles.geography',
        admin: true,
        full: true,
        fullBleed: true,
        modules: [MODULES.admin],
      },
    },
    {
      path: '/@:name',
      name: 'public-profile',
      component: () => import('@/views/PublicProfileView.vue'),
      meta: { public: true, profile: true, edgeToEdge: true, titleKey: 'app.titles.profile' },
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
  // A social sign-in appends its token pair to the return URL for clients that
  // cannot read cookies. This console never stores tokens, so they are dropped
  // before anything else sees the address — they must not reach history, a
  // shared link, or a log.
  if (SESSION_TOKEN_PARAMS.some((key) => to.query[key] !== undefined)) {
    return { path: to.path, query: withoutTokenParams(to.query), hash: to.hash, replace: true }
  }
  if (session.loading) await session.load()

  // The module set is a single cached read, shared by every navigation. It
  // must land before the module gate below can decide anything; a failed
  // read leaves the gates open, so the console keeps working.
  const modules = useModulesStore()
  await modules.load()

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

  // A route whose serving module is not mounted is unreachable: send the
  // navigation to a surface that is always served. The fallback is the same
  // one this route would get while signed out, so the outcome never depends
  // on the session state.
  if (!modules.hasAll(to.meta.modules ?? [])) {
    // Signed-in visitors keep their account page when it is served; a chain
    // of disabled modules (profiles + accounts) falls back to sign-in, which
    // the required auth module alone mounts.
    if (session.signedIn && modules.hasAll([MODULES.profiles])) return { name: 'account' }
    return { name: 'sign-in' }
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
