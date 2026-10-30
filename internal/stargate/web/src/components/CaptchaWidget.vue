<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { authApi } from '@/lib/api'

/**
 * Renders the deployment's captcha provider (Cloudflare Turnstile, hCaptcha,
 * or reCAPTCHA) and emits the token. The provider script is loaded on demand
 * and the widget is removed on unmount so repeating a form does not stack
 * widgets.
 */
interface Props {
  provider?: string
  apiKey?: string
}

const props = defineProps<Props>()
const emit = defineEmits<{ verified: [token: string] }>()
const { t } = useI18n()

const container = ref<HTMLDivElement | null>(null)
const loading = ref(true)
const error = ref('')

let providerName = ''
let siteKey = ''
let widgetId: string | number | null = null

interface CaptchaWindow extends Window {
  turnstile?: {
    render: (container: HTMLElement, options: Record<string, unknown>) => string
    remove?: (widgetId: string) => void
  }
  hcaptcha?: {
    render: (container: HTMLElement, options: Record<string, unknown>) => number
    remove?: (widgetId: number) => void
  }
  grecaptcha?: {
    render: (container: HTMLElement, options: Record<string, unknown>) => number
    reset?: (widgetId: number) => void
  }
}

function loadScript(src: string, id: string): Promise<void> {
  const existing = document.getElementById(id) as HTMLScriptElement | null
  if (existing) {
    if (existing.dataset.loaded === 'true') return Promise.resolve()
    return new Promise((resolve, reject) => {
      existing.addEventListener('load', () => resolve(), { once: true })
      existing.addEventListener('error', () => reject(new Error(t('captchaWidget.loadFailed', { src }))), {
        once: true,
      })
    })
  }

  return new Promise((resolve, reject) => {
    const script = document.createElement('script')
    script.id = id
    script.src = src
    script.async = true
    script.defer = true
    script.onload = () => {
      script.dataset.loaded = 'true'
      resolve()
    }
    script.onerror = () => reject(new Error(t('captchaWidget.loadFailed', { src })))
    document.head.appendChild(script)
  })
}

function cleanup() {
  const w = window as unknown as CaptchaWindow
  if (widgetId === null) return
  if (providerName === 'cloudflare') w.turnstile?.remove?.(String(widgetId))
  if (providerName === 'hcaptcha') w.hcaptcha?.remove?.(Number(widgetId))
  if (providerName === 'recaptcha') w.grecaptcha?.reset?.(Number(widgetId))
  widgetId = null
}

async function render() {
  if (!container.value || !providerName || !siteKey) return
  cleanup()
  container.value.innerHTML = ''
  error.value = ''
  loading.value = true

  try {
    const w = window as unknown as CaptchaWindow
    if (providerName === 'cloudflare') {
      await loadScript(
        'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit',
        'captcha-turnstile',
      )
      if (!w.turnstile) throw new Error(t('captchaWidget.initFailed', { provider: 'Turnstile' }))
      widgetId = w.turnstile.render(container.value, {
        sitekey: siteKey,
        callback: (token: string) => emit('verified', token),
        'error-callback': () => {
          loading.value = false
          error.value = t('captchaWidget.unavailable')
        },
      })
    } else if (providerName === 'hcaptcha') {
      await loadScript('https://js.hcaptcha.com/1/api.js?render=explicit', 'captcha-hcaptcha')
      if (!w.hcaptcha) throw new Error(t('captchaWidget.initFailed', { provider: 'hCaptcha' }))
      widgetId = w.hcaptcha.render(container.value, {
        sitekey: siteKey,
        callback: (token: string) => emit('verified', token),
        'error-callback': () => {
          loading.value = false
          error.value = t('captchaWidget.unavailable')
        },
      })
    } else if (providerName === 'recaptcha') {
      await loadScript(
        'https://www.google.com/recaptcha/api.js?render=explicit',
        'captcha-recaptcha',
      )
      if (!w.grecaptcha) throw new Error(t('captchaWidget.initFailed', { provider: 'reCAPTCHA' }))
      widgetId = w.grecaptcha.render(container.value, {
        sitekey: siteKey,
        callback: (token: string) => emit('verified', token),
        'error-callback': () => {
          loading.value = false
          error.value = t('captchaWidget.unavailable')
        },
      })
    } else {
      throw new Error(t('captchaWidget.noProvider'))
    }
  } catch (caught) {
    error.value = caught instanceof Error ? caught.message : t('captchaWidget.challengeFailed')
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  providerName = props.provider ?? ''
  siteKey = props.apiKey ?? ''
  if (!providerName || !siteKey) {
    try {
      const config = await authApi.captchaConfig()
      providerName = providerName || config.provider
      siteKey = siteKey || config.apiKey
    } catch {
      loading.value = false
      error.value = t('captchaWidget.challengeFailed')
      return
    }
  }
  await render()
})

onUnmounted(cleanup)
</script>

<template>
  <div class="flex min-h-[68px] w-full flex-col items-center justify-center gap-2">
    <span v-if="loading" class="skeleton h-3 w-32" aria-hidden="true" />
    <p v-else-if="error" class="text-center text-2xs text-error">{{ error }}</p>
    <div ref="container" class="flex w-full justify-center" />
  </div>
</template>
