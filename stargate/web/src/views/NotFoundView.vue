<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'

const { t } = useI18n()
</script>

<template>
  <div>
    <h1 class="title mt-2 text-2xl text-base-content">{{ t('notFound.title') }}</h1>
    <p class="voice mt-3">
      {{ t('notFound.body') }}
    </p>
    <div class="mt-6 flex gap-3">
      <RouterLink to="/account" class="btn btn-primary btn-sm">{{ t('notFound.goToAccount') }}</RouterLink>
      <RouterLink to="/activity" class="btn btn-ghost btn-sm">{{ t('notFound.activity') }}</RouterLink>
    </div>
  </div>
</template>
