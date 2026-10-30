<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'

import { useSessionStore } from '@/stores/session'
import { useNoticeStore } from '@/stores/notice'

/**
 * The landing page for a social round-trip that named no return URL: Stargate
 * sends a login here when its `returnUrl` is empty. The session cookie is set
 * before this page mounts — and the global guard has already dropped the token
 * pair the server appends for clients that cannot read cookies — so the only
 * work is to read the session and continue. Nothing is stored here.
 *
 * It exists so a provider return can never dead-end on the not-found page.
 */
const { t } = useI18n()
const router = useRouter()
const session = useSessionStore()
const notice = useNoticeStore()

const problem = ref('')

onMounted(async () => {
  await session.load()
  if (session.signedIn) {
    notice.ok(t('signIn.noticeSignedIn'))
    await router.replace('/account')
    return
  }
  problem.value = t('landing.noSession')
  await router.replace({ name: 'sign-in' })
})
</script>

<template>
  <div>
    <div class="mb-8">
      <h1 class="title mt-2 text-2xl text-base-content">{{ t('landing.title') }}</h1>
      <p class="voice mt-2">
        {{ problem || t('landing.intro') }}
      </p>
    </div>

    <p v-if="!problem" class="flex items-center gap-2 text-2xs text-faint">
      <span class="dot animate-pulse bg-info" aria-hidden="true" />
      {{ t('landing.waiting') }}
    </p>

    <p class="voice mt-6 text-[12px]">
      <RouterLink to="/sign-in" class="link-quiet">{{ t('landing.backToSignIn') }}</RouterLink>
    </p>
  </div>
</template>
