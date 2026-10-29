<template>
  <BaseDialog :show="show" :title="t('admin.accounts.cacheReadAdjustment.title')" width="wide" @close="close">
    <div v-if="loading" class="flex h-48 items-center justify-center" role="status">
      <Icon name="refresh" class="animate-spin text-gray-400" />
    </div>
    <div v-else-if="loadError" class="space-y-4">
      <p class="text-sm text-red-600" role="alert">{{ loadError }}</p>
      <button class="btn btn-secondary" @click="load">{{ t('common.tryAgain') }}</button>
    </div>
    <form v-else id="cache-read-adjustment-form" class="space-y-5" @submit.prevent="save">
      <div class="flex items-center justify-between gap-4">
        <label class="text-sm font-medium" for="cache-adjustment-enabled">{{ t('admin.accounts.cacheReadAdjustment.enabled') }}</label>
        <Toggle id="cache-adjustment-enabled" v-model="form.enabled" :disabled="saving" />
      </div>
      <div>
        <label for="cache-adjustment-percent" class="input-label">{{ t('admin.accounts.cacheReadAdjustment.percent') }}</label>
        <div class="flex items-center gap-2">
          <input id="cache-adjustment-percent" v-model.number="form.percent" type="number" min="0" max="100" step="1" required class="input w-28" :disabled="saving" />
          <span class="text-sm text-gray-500">%</span>
        </div>
      </div>
      <div class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-600">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <span class="text-sm font-medium">{{ t('admin.accounts.cacheReadAdjustment.accounts', { count: form.account_ids.length }) }}</span>
          <div class="flex gap-2">
            <button v-if="selectedIds.length" type="button" class="btn btn-secondary btn-sm" :disabled="saving" @click="addSelected">
              <Icon name="plus" size="xs" class="mr-1" />
              {{ t('admin.accounts.cacheReadAdjustment.addSelected') }}
            </button>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="saving || !form.account_ids.length" @click="form.account_ids = []">{{ t('admin.accounts.cacheReadAdjustment.clear') }}</button>
          </div>
        </div>
        <input v-model="search" class="input" type="search" :placeholder="t('admin.accounts.searchAccounts')" :aria-label="t('admin.accounts.searchAccounts')" :disabled="saving" />
        <p v-if="accountError" class="text-sm text-red-600" role="alert">{{ accountError }}</p>
        <div class="h-64 overflow-y-auto border-y border-gray-200 dark:border-dark-600" :aria-busy="accountsLoading">
          <div v-if="accountsLoading" class="flex h-full items-center justify-center"><Icon name="refresh" class="animate-spin text-gray-400" /></div>
          <label v-for="account in accountsLoading ? [] : accounts" :key="account.id" class="flex min-h-11 cursor-pointer items-center gap-3 border-b border-gray-100 px-2 py-2 text-sm last:border-0 hover:bg-gray-50 dark:border-dark-700 dark:hover:bg-dark-700">
            <input v-model="form.account_ids" type="checkbox" :value="account.id" class="checkbox" :disabled="saving" />
            <span class="min-w-0 flex-1 break-words">{{ account.name }}</span>
            <span class="shrink-0 text-xs text-gray-500">{{ account.platform }} #{{ account.id }}</span>
          </label>
          <p v-if="!accountsLoading && !accounts.length" class="p-4 text-center text-sm text-gray-500">{{ t('common.noData') }}</p>
        </div>
        <div class="flex items-center justify-between text-xs text-gray-500">
          <span>{{ page }} / {{ totalPages }}</span>
          <div class="flex gap-2">
            <button type="button" class="btn btn-secondary btn-sm" :title="t('common.back')" :aria-label="t('common.back')" :disabled="page <= 1 || accountsLoading" @click="page--"><Icon name="chevronLeft" size="sm" /></button>
            <button type="button" class="btn btn-secondary btn-sm" :title="t('common.next')" :aria-label="t('common.next')" :disabled="page >= totalPages || accountsLoading" @click="page++"><Icon name="chevronRight" size="sm" /></button>
          </div>
        </div>
      </div>
      <p v-if="saveError" class="text-sm text-red-600" role="alert">{{ saveError }}</p>
    </form>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button class="btn btn-secondary" :disabled="saving" @click="close">{{ t('common.cancel') }}</button>
        <button type="submit" form="cache-read-adjustment-form" class="btn btn-primary" :disabled="loading || !!loadError || saving || !validPercent">
          <Icon :name="saving ? 'refresh' : 'check'" size="sm" class="mr-1.5" :class="{ 'animate-spin': saving }" />{{ t('common.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { CacheReadAdjustmentSettings } from '@/api/admin/settings'
import type { Account } from '@/types'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{ show: boolean; selectedIds: number[] }>()
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()
const appStore = useAppStore()
const form = ref<CacheReadAdjustmentSettings>({ enabled: false, percent: 10, account_ids: [] })
const loading = ref(false)
const saving = ref(false)
const loadError = ref('')
const saveError = ref('')
const accountError = ref('')
const accounts = ref<Account[]>([])
const accountsLoading = ref(false)
const search = ref('')
const page = ref(1)
const totalPages = ref(1)
const validPercent = computed(() => Number.isInteger(form.value.percent) && form.value.percent >= 0 && form.value.percent <= 100)
let controller: AbortController | undefined
let searchTimer: ReturnType<typeof setTimeout> | undefined
let loadVersion = 0

function close() {
  if (!saving.value) emit('close')
}

async function loadAccounts() {
  controller?.abort()
  const request = new AbortController()
  controller = request
  accountsLoading.value = true
  accountError.value = ''
  try {
    const result = await adminAPI.accounts.list(page.value, 25, { search: search.value, lite: 'true' }, { signal: request.signal })
    if (request.signal.aborted) return
    accounts.value = result.items
    totalPages.value = Math.max(1, Math.ceil(result.total / 25))
  } catch (error) {
    if (!request.signal.aborted) accountError.value = extractApiErrorMessage(error, t('admin.accounts.cacheReadAdjustment.loadFailed'))
  } finally {
    if (!request.signal.aborted) accountsLoading.value = false
  }
}

async function load() {
  const version = ++loadVersion
  loading.value = true
  loadError.value = ''
  saveError.value = ''
  try {
    const settings = await adminAPI.settings.getCacheReadAdjustmentSettings()
    if (version !== loadVersion) return
    form.value = { ...settings, account_ids: [...(settings.account_ids ?? [])] }
    await loadAccounts()
  } catch (error) {
    if (version === loadVersion) loadError.value = extractApiErrorMessage(error, t('admin.accounts.cacheReadAdjustment.loadFailed'))
  } finally {
    if (version === loadVersion) loading.value = false
  }
}

function addSelected() {
  form.value.account_ids = [...new Set([...form.value.account_ids, ...props.selectedIds])]
}

async function save() {
  if (!validPercent.value || saving.value) return
  saving.value = true
  saveError.value = ''
  try {
    await adminAPI.settings.updateCacheReadAdjustmentSettings({ ...form.value, account_ids: [...form.value.account_ids] })
    appStore.showSuccess(t('admin.accounts.cacheReadAdjustment.saved'))
    emit('close')
  } catch (error) {
    saveError.value = extractApiErrorMessage(error, t('admin.accounts.cacheReadAdjustment.saveFailed'))
  } finally {
    saving.value = false
  }
}

watch(() => props.show, show => {
  if (show) {
    search.value = ''
    page.value = 1
    void load()
  } else {
    loadVersion++
    controller?.abort()
    clearTimeout(searchTimer)
  }
}, { immediate: true })
watch(search, () => {
  clearTimeout(searchTimer)
  controller?.abort()
  searchTimer = setTimeout(() => {
    if (!props.show) return
    if (page.value !== 1) page.value = 1
    else void loadAccounts()
  }, 250)
})
watch(page, () => { if (props.show && !loading.value) void loadAccounts() })
onBeforeUnmount(() => { controller?.abort(); clearTimeout(searchTimer) })
</script>
