<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Renew16 } from '@carbon/icons-vue'
import { RouterLink } from 'vue-router'

import { adminApi } from '@/lib/api'
import { StargateError } from '@/lib/api/client'
import type { AdminRealm } from '@/lib/api/types'
import { relative, shortId } from '@/lib/format'
import PageHeader from '@/components/PageHeader.vue'
import AdminNav from '@/components/AdminNav.vue'
import ConsolePanel from '@/components/ConsolePanel.vue'
import LoadingSkeleton from '@/components/LoadingSkeleton.vue'
import IconButton from '@/components/IconButton.vue'
import TextField from '@/components/TextField.vue'

const { t } = useI18n()

const realms = ref<AdminRealm[]>([])
const total = ref(0)
const query = ref('')
const verifiedOnly = ref(false)
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const page = await adminApi.realms({
      query: query.value.trim() || undefined,
      verified: verifiedOnly.value ? true : undefined,
      take: 50,
    })
    realms.value = page.items
    total.value = page.total
  } catch (caught) {
    error.value = caught instanceof StargateError ? caught.message : t('admin.realms.errorLoad')
  } finally {
    loading.value = false
  }
}

function clearFilters() {
  query.value = ''
  verifiedOnly.value = false
  void load()
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader :title="t('admin.realms.title')" :description="t('admin.realms.description')">
      <template #actions>
        <IconButton :label="t('common.refresh')" :disabled="loading" @click="load">
          <Renew16 aria-hidden="true" />
        </IconButton>
      </template>
    </PageHeader>

    <AdminNav />

    <form class="mb-5 flex flex-wrap items-end gap-3" @submit.prevent="load">
      <TextField
        id="realm-query"
        v-model="query"
        root-class="min-w-[200px] flex-1"
        :label="t('admin.realms.query')"
        :placeholder="t('admin.realms.queryPlaceholder')"
      />
      <label class="flex h-12 items-center gap-2 text-xs text-dim">
        <input v-model="verifiedOnly" class="checkbox checkbox-xs" type="checkbox" />
        {{ t('admin.realms.verifiedOnly') }}
      </label>
      <button type="submit" class="btn btn-primary btn-field" :disabled="loading">
        {{ t('common.search') }}
      </button>
      <button type="button" class="btn btn-ghost btn-field" @click="clearFilters">
        {{ t('common.clear') }}
      </button>
    </form>

    <p v-if="error" class="note note-error mb-4">{{ error }}</p>

    <ConsolePanel :title="t('admin.realms.panelTitle')" :note="t('admin.realms.total', { n: total })">
      <LoadingSkeleton v-if="loading" :rows="5" :columns="[3, 2, 1, 1]" header />

      <p v-else-if="!realms.length" class="voice py-6">{{ t('admin.realms.empty') }}</p>

      <div v-else class="table-bleed overflow-x-auto">
        <table class="ledger">
          <thead>
            <tr>
              <th scope="col">{{ t('admin.realms.colRealm') }}</th>
              <th scope="col">{{ t('admin.realms.colOwner') }}</th>
              <th scope="col">{{ t('admin.realms.colBoost') }}</th>
              <th scope="col">{{ t('admin.realms.colState') }}</th>
              <th scope="col">{{ t('admin.realms.colUpdated') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="realm in realms" :key="realm.id">
              <td>
                <RouterLink
                  :to="`/admin/realms/${encodeURIComponent(realm.slug)}`"
                  class="text-xs text-base-content hover:underline"
                >
                  {{ realm.name }}
                </RouterLink>
                <p class="mt-0.5 font-mono text-2xs text-faint">/{{ realm.slug }}</p>
              </td>
              <td class="font-mono text-2xs text-dim">{{ shortId(realm.account_id) }}</td>
              <td class="text-xs text-dim">
                {{ realm.boost_points }} · {{ t('admin.realms.level', { n: realm.boost_level }) }}
              </td>
              <td>
                <div class="flex flex-wrap gap-1">
                  <span v-if="realm.is_public" class="chip">{{ t('admin.realms.public') }}</span>
                  <span v-if="realm.is_community" class="chip">
                    {{ t('admin.realms.community') }}
                  </span>
                  <span v-if="realm.verification" class="chip chip-active">
                    {{ t('admin.realms.verified') }}
                  </span>
                </div>
              </td>
              <td class="text-xs text-dim">{{ relative(realm.updated_at) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </ConsolePanel>
  </div>
</template>
