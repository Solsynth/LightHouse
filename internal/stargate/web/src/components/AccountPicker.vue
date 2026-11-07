<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { accountApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { Account } from '@/lib/api/types'
import AccountAvatar from '@/components/AccountAvatar.vue'
import TextField from '@/components/TextField.vue'

/**
 * A searchable account field. Type a name or nickname, pick from the results,
 * and the selected account is shown with its avatar. The parent owns the
 * selected account through `v-model`; typing after a selection is a change, so
 * the clear action hands the field back for a new search.
 */
const props = withDefaults(
  defineProps<{
    modelValue: Account | null
    label?: string
    placeholder?: string
    hint?: string
    disabled?: boolean
    excludeIds?: string[]
  }>(),
  { label: '', placeholder: '', disabled: false },
)

const emit = defineEmits<{ 'update:modelValue': [Account | null] }>()

const { t } = useI18n()
const listboxId = useId()
const field = ref<InstanceType<typeof TextField> | null>(null)
const inputId = useId()
const query = ref('')
const results = ref<Account[]>([])
const searching = ref(false)
const error = ref('')
const open = ref(false)
const active = ref(-1)

let timer: number | undefined
let requestSeq = 0

const selected = computed(() => props.modelValue)
const selectedName = computed(() => selected.value?.nick || selected.value?.name || '')

function displayName(account: Account): string {
  return account.nick || account.name
}

function select(account: Account) {
  emit('update:modelValue', account)
  query.value = ''
  results.value = []
  open.value = false
  active.value = -1
}

function clear() {
  emit('update:modelValue', null)
  query.value = ''
  void nextTick(() => field.value?.focus())
}

function scheduleSearch() {
  window.clearTimeout(timer)
  const term = query.value.trim()
  if (!term || selected.value) {
    results.value = []
    open.value = false
    active.value = -1
    return
  }
  timer = window.setTimeout(() => {
    void search(term)
  }, 220)
}

async function search(term: string) {
  const seq = ++requestSeq
  searching.value = true
  error.value = ''
  open.value = true
  try {
    const found = await accountApi.search(term, 12)
    if (seq !== requestSeq) return
    const excluded = new Set(props.excludeIds ?? [])
    results.value = found.filter((account) => !excluded.has(account.id)).slice(0, 8)
    active.value = results.value.length ? 0 : -1
  } catch (caught) {
    if (seq !== requestSeq) return
    error.value = caught instanceof StargateError ? caught.message : t('common.accountPicker.error')
    results.value = []
    active.value = -1
  } finally {
    if (seq === requestSeq) searching.value = false
  }
}

function onFocus() {
  if (!selected.value && results.value.length) open.value = true
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'ArrowDown') {
    if (!open.value && query.value.trim()) {
      scheduleSearch()
      open.value = true
      event.preventDefault()
      return
    }
    if (results.value.length) {
      open.value = true
      active.value = Math.min(active.value + 1, results.value.length - 1)
      event.preventDefault()
    }
    return
  }
  if (event.key === 'ArrowUp') {
    if (results.value.length) {
      active.value = Math.max(active.value - 1, 0)
      event.preventDefault()
    }
    return
  }
  if (event.key === 'Enter' && open.value && active.value >= 0) {
    const account = results.value[active.value]
    if (account) {
      event.preventDefault()
      select(account)
    }
    return
  }
  if (event.key === 'Escape') {
    open.value = false
    active.value = -1
  }
}

let closeTimer: number | undefined
function closeSoon() {
  window.clearTimeout(closeTimer)
  closeTimer = window.setTimeout(() => {
    open.value = false
    active.value = -1
  }, 140)
}

watch(query, scheduleSearch)
</script>

<template>
  <div class="relative">
    <div
      v-if="selected"
      class="flex min-h-12 items-center gap-2.5 rounded-input bg-base-300/55 px-3 py-2"
    >
      <AccountAvatar :account="selected" />
      <div class="min-w-0 flex-1">
        <p class="truncate text-xs font-semibold text-base-content">{{ selectedName }}</p>
        <p class="truncate text-2xs text-faint">@{{ selected.name }}</p>
      </div>
      <button type="button" class="btn btn-ghost btn-xs" :disabled="disabled" @click="clear">
        {{ t('common.accountPicker.change') }}
      </button>
    </div>
    <TextField
      v-else
      :id="inputId"
      ref="field"
      v-model="query"
      :label="label"
      :placeholder="placeholder || t('common.accountPicker.searchPlaceholder')"
      :hint="hint"
      :disabled="disabled"
      :aria-expanded="open"
      :aria-controls="listboxId"
      :aria-activedescendant="active >= 0 ? `${listboxId}-${active}` : undefined"
      aria-autocomplete="list"
      role="combobox"
      autocapitalize="off"
      spellcheck="false"
      @focus="onFocus"
      @blur="closeSoon"
      @keydown="onKeydown"
    />

    <div
      v-if="open && !selected"
      :id="listboxId"
      class="absolute z-30 mt-1.5 w-full overflow-hidden rounded-card bg-base-100 p-1 shadow-[0_24px_50px_-28px_rgb(0_0_0/0.55)]"
      role="listbox"
    >
      <p v-if="searching" class="px-3 py-2.5 text-2xs text-faint">
        {{ t('common.accountPicker.searching') }}
      </p>
      <p v-else-if="error" class="px-3 py-2.5 text-2xs text-error">{{ error }}</p>
      <p v-else-if="!results.length" class="px-3 py-2.5 text-2xs text-faint">
        {{ t('common.accountPicker.noResults') }}
      </p>
      <button
        v-for="(account, index) in results"
        :id="`${listboxId}-${index}`"
        :key="account.id"
        type="button"
        role="option"
        :aria-selected="index === active"
        class="flex w-full items-center gap-2.5 rounded-chip px-2.5 py-2 text-left transition-colors"
        :class="index === active ? 'bg-base-300' : 'hover:bg-base-300/60'"
        @mousedown.prevent
        @mouseenter="active = index"
        @click="select(account)"
      >
        <AccountAvatar :account="account" />
        <span class="min-w-0">
          <span class="block truncate text-xs text-base-content">{{ displayName(account) }}</span>
          <span class="block truncate text-2xs text-faint">@{{ account.name }}</span>
        </span>
      </button>
    </div>
  </div>
</template>
