<script setup lang="ts">
import { Close16 } from '@carbon/icons-vue'
import { useI18n } from 'vue-i18n'

import { useNoticeStore } from '@/stores/notice'

/**
 * The shell's status toast. It floats above the page and the elevation sheet
 * so a result is visible wherever the action happened, keeps the console's
 * `OK` / `ERROR` marker, and lets a reader dismiss any item without waiting
 * for its deadline. Errors announce assertively; confirmations do not.
 */
const notice = useNoticeStore()
const { t } = useI18n()
</script>

<template>
  <div
    class="pointer-events-none fixed inset-x-0 bottom-[max(1rem,env(safe-area-inset-bottom))] z-[1100] flex justify-center px-4"
  >
    <TransitionGroup
      tag="div"
      class="flex w-full max-w-[min(28rem,calc(100vw-2rem))] flex-col gap-2"
      enter-active-class="transition duration-200 ease-out"
      enter-from-class="translate-y-2 opacity-0"
      leave-active-class="transition duration-150 ease-in"
      leave-to-class="translate-y-1 opacity-0"
    >
      <div
        v-for="item in notice.items"
        :key="item.id"
        class="pointer-events-auto flex items-start gap-2.5 rounded-card bg-base-200 px-4 py-3 shadow-[0_18px_44px_-20px_rgb(0_0_0/0.5)]"
        :role="item.tone === 'error' ? 'alert' : 'status'"
      >
        <span
          class="mt-0.5 shrink-0 text-2xs font-semibold tracking-[0.12em]"
          :class="item.tone === 'error' ? 'text-error' : 'text-success'"
        >
          {{ item.tone === 'error' ? 'ERROR' : 'OK' }}
        </span>
        <p class="min-w-0 flex-1 text-xs leading-relaxed text-base-content">
          {{ item.message }}
        </p>
        <button
          type="button"
          class="-mr-1.5 -mt-0.5 grid size-5 shrink-0 place-items-center rounded-full text-faint transition-colors hover:bg-base-300/70 hover:text-base-content"
          :aria-label="t('common.dismiss')"
          :title="t('common.dismiss')"
          @click="notice.dismiss(item.id)"
        >
          <Close16 class="size-3" aria-hidden="true" />
        </button>
      </div>
    </TransitionGroup>
  </div>
</template>
