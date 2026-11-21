import { createRouter, createWebHistory } from 'vue-router'

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    {
      path: '/',
      name: 'overview',
      component: () => import('@/views/OverviewView.vue'),
      meta: { title: 'Overview', requiresAuth: true },
    },
    {
      path: '/history',
      name: 'history',
      component: () => import('@/views/HistoryView.vue'),
      meta: { title: 'Scrobble History', requiresAuth: true },
    },
    {
      path: '/collections',
      name: 'collections',
      component: () => import('@/views/CollectionsView.vue'),
      meta: { title: 'Collections & Shelves', requiresAuth: true },
    },
    {
      path: '/collections/:id',
      name: 'collection-detail',
      component: () => import('@/views/CollectionDetailView.vue'),
      meta: { title: 'Collection', requiresAuth: true },
    },
    {
      path: '/catalog',
      name: 'catalog',
      component: () => import('@/views/CatalogView.vue'),
      meta: { title: 'Catalog' },
    },
    {
      path: '/catalog/:id',
      name: 'catalog-item',
      component: () => import('@/views/CatalogItemView.vue'),
      meta: { title: 'Media Item' },
    },
    {
      path: '/activities',
      name: 'activities',
      component: () => import('@/views/ActivitiesView.vue'),
      meta: { title: 'Activities', requiresAuth: true },
    },
    {
      path: '/rewind/:year?',
      name: 'rewind',
      component: () => import('@/views/RewindView.vue'),
      meta: { title: 'Rewind', requiresAuth: true },
    },
    {
      path: '/settings',
      name: 'settings',
      component: () => import('@/views/SettingsView.vue'),
      meta: { title: 'Settings', requiresAuth: true },
    },
    {
      path: '/:pathMatch(.*)*',
      redirect: '/',
    },
  ],
})

router.afterEach((to) => {
  const title = to.meta?.title ? `${to.meta.title} · Pulsar` : 'Pulsar Manage'
  document.title = title
})

export default router
