<script setup lang="ts">
import { RouterLink } from 'vue-router'
</script>

<template>
  <div>
    <h1 class="title mt-2 text-2xl text-base-content">No Such Route</h1>
    <p class="voice mt-3">
      This address does not match a console page. The API may still serve it — check the path, or
      start from your account.
    </p>
    <div class="mt-6 flex gap-3">
      <RouterLink to="/account" class="btn btn-primary btn-sm">Go to Account</RouterLink>
      <RouterLink to="/activity" class="btn btn-ghost btn-sm">Activity</RouterLink>
    </div>
  </div>
</template>
