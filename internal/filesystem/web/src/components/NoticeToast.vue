<script setup lang="ts">
/**
 * Transient notices: upload failures, rename results, permission errors. The
 * shell footer that used to carry them is gone, so they surface as a short
 * stack under the app bar instead of pushing the working area around.
 */
import { Close16 } from '@carbon/icons-vue'
import { useI18n } from 'vue-i18n'

import { useNoticeStore } from '@/stores/notice'

const notice = useNoticeStore()
const { t } = useI18n()
</script>

<template>
  <div
    class="pointer-events-none fixed bottom-4 left-1/2 z-[60] flex w-[340px] max-w-[calc(100vw-24px)] -translate-x-1/2 flex-col gap-1.5"
    role="status"
    aria-live="polite"
  >
    <TransitionGroup
      enter-active-class="transition duration-200 ease-out"
      enter-from-class="translate-y-1 opacity-0"
      leave-active-class="transition duration-150 ease-in"
      leave-to-class="opacity-0"
    >
      <div
        v-for="item in notice.items"
        :key="item.id"
        class="pointer-events-auto flex items-start gap-2 rounded-card bg-base-200 px-3 py-2 shadow-[0_18px_40px_-24px_rgb(0_0_0/0.55)]"
      >
        <span
          class="mt-1 size-1.5 shrink-0 rounded-full"
          :class="
            item.tone === 'error'
              ? 'bg-error'
              : item.tone === 'ok'
                ? 'bg-success'
                : 'bg-primary'
          "
          aria-hidden="true"
        />
        <span class="min-w-0 flex-1 text-[11.5px] leading-4 text-base-content">
          {{ item.message }}
        </span>
        <button
          type="button"
          class="-mr-1 grid size-5 shrink-0 place-items-center rounded-full text-faint transition-colors hover:bg-base-300/70 hover:text-base-content"
          :aria-label="t('app.dismissNotice')"
          @click="notice.dismiss(item.id)"
        >
          <Close16 class="h-3 w-3" aria-hidden="true" />
        </button>
      </div>
    </TransitionGroup>
  </div>
</template>
