import './assets/main.css'

import { createApp, nextTick } from 'vue'
import { lazyLoad } from 'unlazy'

import App from './App.vue'
import router from './router'

let unlazyObserver: MutationObserver | null = null

function refreshLazyImages() {
  lazyLoad('img[data-src], img[loading="lazy"]')
}

function observeLazyImages() {
  refreshLazyImages()
  if (unlazyObserver || typeof MutationObserver === 'undefined') return
  unlazyObserver = new MutationObserver(() => {
    refreshLazyImages()
  })
  unlazyObserver.observe(document.body, { childList: true, subtree: true })
}

const app = createApp(App)
app.use(router)
app.mount('#app')

nextTick(() => {
  observeLazyImages()
})
