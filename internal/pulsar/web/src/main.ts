import { createApp, defineAsyncComponent, type Component } from 'vue'
import { createPinia } from 'pinia'

import '@fontsource/nunito/400.css'
import '@fontsource/nunito/600.css'
import '@fontsource/nunito/700.css'
import '@fontsource/ibm-plex-mono/400.css'
import '@fontsource/ibm-plex-mono/500.css'
import './assets/main.css'

const ISLANDS: Record<string, () => Promise<{ default: Component }>> = {
  'now-playing': () => import('./islands/NowPlayingIsland.vue'),
  'quick-stats': () => import('./islands/QuickStatsIsland.vue'),
  'recent-scrobbles': () => import('./islands/RecentScrobblesIsland.vue'),
  'catalog-search': () => import('./islands/CatalogSearchIsland.vue'),
  'history-manager': () => import('./islands/HistoryManagerIsland.vue'),
  'collections-manager': () => import('./islands/CollectionsManagerIsland.vue'),
  'collection-detail': () => import('./islands/CollectionDetailIsland.vue'),
  'media-actions': () => import('./islands/MediaActionsIsland.vue'),
  'media-reviews': () => import('./islands/MediaReviewsIsland.vue'),
  'catalog-browser': () => import('./islands/CatalogBrowserIsland.vue'),
  'activities-panel': () => import('./islands/ActivitiesIsland.vue'),
  'user-history': () => import('./islands/UserHistoryIsland.vue'),
  'rewind-view': () => import('./islands/RewindViewIsland.vue'),
  'settings-panel': () => import('./islands/SettingsPanelIsland.vue'),
}

const pinia = createPinia()

export function mountIslands(root: ParentNode = document): void {
  const nodes = root.querySelectorAll<HTMLElement>('[data-island]')
  for (const el of nodes) {
    if (el.dataset.mounted === 'true') continue
    const name = el.dataset.island
    if (!name || !(name in ISLANDS)) continue

    let props: Record<string, unknown> = {}
    if (el.dataset.props) {
      try {
        props = JSON.parse(el.dataset.props) as Record<string, unknown>
      } catch {
        // ignore malformed props
      }
    }

    const loader = ISLANDS[name]
    if (!loader) continue
    el.dataset.mounted = 'true'
    const AsyncComp = defineAsyncComponent(loader)
    const app = createApp(AsyncComp, props)
    app.use(pinia)
    app.mount(el)
  }
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', () => mountIslands())
} else {
  mountIslands()
}
