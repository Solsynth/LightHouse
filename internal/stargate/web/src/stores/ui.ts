import { computed, ref } from 'vue'
import { defineStore } from 'pinia'

import { i18n } from '@/lib/i18n'
import {
  LOCALE_KEY,
  THEME_KEY,
  applyLocale,
  applyTheme,
  readLocale,
  readTheme,
  type Locale,
  type Theme,
} from '@/lib/prefs'

/** Interface preferences shared by the shell and every view. */
export const useUiStore = defineStore('ui', () => {
  const theme = ref<Theme>(readTheme())
  const locale = ref<Locale>(readLocale())

  /**
   * Backdrop the open page lends to the shell. The consent screen publishes
   * the requesting app's own background here so the app's identity, not the
   * account's, paints behind the card; the shell falls back to the account
   * background while this is empty. The publishing page clears it on unmount.
   */
  const appBackdrop = ref('')

  const isDark = computed(() => theme.value === 'dark')

  function setTheme(next: Theme) {
    theme.value = next
    localStorage.setItem(THEME_KEY, next)
    applyTheme(next)
  }

  function toggleTheme() {
    setTheme(theme.value === 'dark' ? 'light' : 'dark')
  }

  function setLocale(next: Locale) {
    locale.value = next
    i18n.global.locale.value = next
    localStorage.setItem(LOCALE_KEY, next)
    applyLocale(next)
  }

  function setAppBackdrop(url: string) {
    appBackdrop.value = url
  }

  return {
    theme,
    locale,
    isDark,
    appBackdrop,
    setTheme,
    toggleTheme,
    setLocale,
    setAppBackdrop,
  }
})
