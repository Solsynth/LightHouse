<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Checkmark16, Copy16 } from '@carbon/icons-vue'

const props = defineProps<{
  value: string
  label: 'source' | 'publicLink' | 'id'
}>()

const { t } = useI18n()
const copied = ref(false)

const title = computed(() => {
  if (props.label === 'source') return t('preview.copySource')
  if (props.label === 'id') return t('preview.copyId')
  return t('preview.copyLink')
})

async function copy() {
  try {
    await navigator.clipboard.writeText(props.value)
    copied.value = true
    window.setTimeout(() => {
      copied.value = false
    }, 2000)
  } catch {
    copied.value = false
  }
}
</script>

<template>
  <button
    type="button"
    class="inline-flex h-9 shrink-0 items-center justify-center rounded-control bg-base-200/70 px-2.5 text-dim transition-colors hover:bg-base-300 hover:text-base-content"
    :title="title"
    :aria-label="title"
    @click="copy"
  >
    <Checkmark16 v-if="copied" aria-hidden="true" />
    <Copy16 v-else aria-hidden="true" />
  </button>
</template>
