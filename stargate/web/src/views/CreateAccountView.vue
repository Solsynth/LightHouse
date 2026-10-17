<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'

import { useNoticeStore } from '@/stores/notice'
import { authApi, signInApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import CaptchaWidget from '@/components/CaptchaWidget.vue'
import type { CaptchaConfig } from '@/lib/api/types'

const router = useRouter()
const notice = useNoticeStore()

const form = ref({
  name: '',
  nick: '',
  email: '',
  password: '',
  language: 'en-us',
})
const captcha = ref<CaptchaConfig | null>(null)
const captchaToken = ref('')
const busy = ref(false)
const error = ref('')
const fieldErrors = ref<Record<string, string[]>>({})
const created = ref(false)

const nameProblem = computed(() => {
  const value = form.value.name
  if (!value) return ''
  if (!/^[A-Za-z0-9_-]{2,256}$/.test(value)) {
    return 'Use 2–256 letters, numbers, underscores, or hyphens.'
  }
  return ''
})

const emailProblem = computed(() => {
  const value = form.value.email
  if (!value) return ''
  if (value.includes('+')) return 'Email addresses cannot contain a plus sign.'
  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value)) return 'That does not look like an email address.'
  return ''
})

const passwordProblem = computed(() => {
  const value = form.value.password
  if (!value) return ''
  if (value.length < 4) return 'At least 4 characters.'
  if (value.length > 128) return 'At most 128 characters.'
  return ''
})

const valid = computed(
  () =>
    form.value.name.length > 1 &&
    form.value.nick.length > 0 &&
    form.value.email.length > 0 &&
    form.value.password.length >= 4 &&
    !nameProblem.value &&
    !emailProblem.value &&
    !passwordProblem.value,
)

async function submit() {
  if (!valid.value) return
  busy.value = true
  error.value = ''
  fieldErrors.value = {}
  try {
    await signInApi.register({ ...form.value, captcha_token: captchaToken.value })
    created.value = true
    notice.ok('Account created. Sign in to continue.')
    await router.push({ name: 'sign-in', query: { next: '/account' } })
  } catch (caught) {
    if (caught instanceof StargateError) {
      fieldErrors.value = caught.fields ?? {}
      error.value = caught.message
    } else {
      error.value = caught instanceof Error ? caught.message : 'The account could not be created.'
    }
  } finally {
    busy.value = false
  }
}

onMounted(async () => {
  try {
    captcha.value = await authApi.captchaConfig()
  } catch {
    captcha.value = null
  }
})
</script>

<template>
  <div>
    <div class="mb-8">
      <h1 class="title mt-2 text-2xl text-base-content">Create an Account</h1>
      <p class="voice mt-2">
        One identity for every Solar Network service. Verify your email afterward to activate it.
      </p>
    </div>

    <form class="border border-line bg-base-200/40 p-5" @submit.prevent="submit">
      <div class="grid gap-4">
        <div>
          <label for="name" class="field-label">Username</label>
          <input
            id="name"
            v-model="form.name"
            class="input"
            autocomplete="username"
            autocapitalize="off"
            spellcheck="false"
            placeholder="littlesheep"
          />
          <p v-if="nameProblem" class="mt-1 text-2xs text-error">{{ nameProblem }}</p>
          <p v-else-if="fieldErrors.name" class="mt-1 text-2xs text-error">
            {{ fieldErrors.name[0] }}
          </p>
        </div>
        <div>
          <label for="nick" class="field-label">Display name</label>
          <input id="nick" v-model="form.nick" class="input" autocomplete="nickname" />
          <p v-if="fieldErrors.nick" class="mt-1 text-2xs text-error">{{ fieldErrors.nick[0] }}</p>
        </div>
      </div>

      <div class="mt-4">
        <label for="email" class="field-label">Email</label>
        <input
          id="email"
          v-model="form.email"
          class="input"
          type="email"
          autocomplete="email"
          autocapitalize="off"
          spellcheck="false"
        />
        <p v-if="emailProblem" class="mt-1 text-2xs text-error">{{ emailProblem }}</p>
        <p v-else-if="fieldErrors.email" class="mt-1 text-2xs text-error">
          {{ fieldErrors.email[0] }}
        </p>
      </div>

      <div class="mt-4">
        <label for="password" class="field-label">Password</label>
        <input
          id="password"
          v-model="form.password"
          class="input"
          type="password"
          autocomplete="new-password"
        />
        <p v-if="passwordProblem" class="mt-1 text-2xs text-error">{{ passwordProblem }}</p>
        <p v-else-if="fieldErrors.password" class="mt-1 text-2xs text-error">
          {{ fieldErrors.password[0] }}
        </p>
      </div>

      <div v-if="captcha" class="mt-4">
        <p class="field-label">Verification</p>
        <CaptchaWidget
          :provider="captcha.provider"
          :api-key="captcha.apiKey"
          @verified="captchaToken = $event"
        />
      </div>

      <p v-if="error" class="mt-4 border-l border-error pl-3 text-xs text-error">{{ error }}</p>

      <button type="submit" class="btn btn-primary mt-5 w-full" :disabled="busy || !valid">
        {{ busy ? 'Creating…' : 'Create Account' }}
      </button>
    </form>

    <p class="voice mt-6 text-[12px]">
      Already registered?
      <RouterLink to="/sign-in" class="link-quiet">Sign In</RouterLink>.
    </p>
  </div>
</template>
