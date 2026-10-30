<template>
  <BaseDialog :show="show" :title="t('admin.accounts.cacheReadAdjustment.title')" width="wide" @close="close">
    <nav class="mb-5 flex gap-1 border-b border-gray-200 dark:border-dark-600" role="tablist">
      <button v-for="item in (['settings', 'stats'] as const)" :key="item" type="button" role="tab" :aria-selected="tab === item" class="border-b-2 px-3 py-2 text-sm" :class="tab === item ? 'border-primary-500 text-primary-600' : 'border-transparent text-gray-500'" @click="tab = item">{{ t(`admin.accounts.cacheReadAdjustment.${item}`) }}</button>
    </nav>
    <div v-if="loading" class="flex h-48 items-center justify-center" role="status">
      <Icon name="refresh" class="animate-spin text-gray-400" />
    </div>
    <div v-else-if="loadError" class="space-y-4">
      <p class="text-sm text-red-600" role="alert">{{ loadError }}</p>
      <button class="btn btn-secondary" @click="load">{{ t('common.tryAgain') }}</button>
    </div>
    <CacheReadAdjustmentStats v-else-if="tab === 'stats'" />
    <div v-else class="space-y-5">
      <div class="flex items-center justify-between gap-4">
        <label class="text-sm font-medium" for="cache-adjustment-enabled">{{ t('admin.accounts.cacheReadAdjustment.enabled') }}</label>
        <div class="flex items-center gap-3">
          <span class="text-xs" :class="settings.enabled ? 'text-emerald-600' : 'text-gray-500'" role="status">{{ t(settings.enabled ? 'admin.accounts.cacheReadAdjustment.active' : 'admin.accounts.cacheReadAdjustment.inactive') }}</span>
          <Toggle id="cache-adjustment-enabled" :model-value="settings.enabled" :disabled="saving" @update:model-value="setEnabled" />
        </div>
      </div>
      <form id="cache-read-adjustment-form" @submit.prevent="savePercent">
        <label for="cache-adjustment-percent" class="input-label">{{ t('admin.accounts.cacheReadAdjustment.percent') }}</label>
        <div class="flex items-center gap-2">
          <input id="cache-adjustment-percent" v-model.number="percent" type="number" min="0" max="100" step="1" required class="input w-28" :disabled="saving" />
          <span class="mr-2 text-sm text-gray-500">%</span>
          <button type="submit" class="btn btn-secondary btn-sm" :disabled="saving || !validPercent || percent === settings.percent">
            <Icon name="check" size="sm" class="mr-1" />{{ t('common.save') }}
          </button>
        </div>
      </form>
      <p v-if="saveError" class="text-sm text-red-600" role="alert">{{ saveError }}</p>

      <section class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-600" :aria-label="t('admin.accounts.cacheReadAdjustment.selectedAccounts')">
        <div class="flex min-h-8 flex-wrap items-center justify-between gap-2">
          <h4 class="text-sm font-medium">{{ t('admin.accounts.cacheReadAdjustment.accounts', { count: settings.account_ids.length }) }}</h4>
          <button type="button" class="btn btn-secondary btn-sm text-red-600" :disabled="saving || !removalIds.length" @click="removeAccounts(removalIds)">
            <Icon name="trash" size="xs" class="mr-1" />{{ t('admin.accounts.cacheReadAdjustment.removeSelected', { count: removalIds.length }) }}
          </button>
        </div>
        <p v-if="selectedError" class="text-sm text-red-600" role="alert">{{ selectedError }} <button type="button" class="underline" @click="loadSelected">{{ t('common.tryAgain') }}</button></p>
        <div class="border-y border-gray-200 dark:border-dark-600" :aria-busy="selectedLoading">
          <label class="flex h-9 items-center gap-3 border-b border-gray-100 px-2 text-xs text-gray-500 dark:border-dark-700">
            <input type="checkbox" class="checkbox" :checked="allSelected" :indeterminate="someSelected && !allSelected" :disabled="saving || selectedLoading || !selectedAccounts.length" :aria-label="t('admin.accounts.cacheReadAdjustment.selectPage')" @change="selectPage(($event.target as HTMLInputElement).checked)" />
            {{ t('admin.accounts.cacheReadAdjustment.selectPage') }}
          </label>
          <div class="max-h-56 overflow-y-auto">
            <div v-if="selectedLoading" class="flex h-24 items-center justify-center"><Icon name="refresh" class="animate-spin text-gray-400" /></div>
            <div v-for="account in selectedLoading ? [] : selectedAccounts" :key="account.id" class="flex min-h-12 items-center gap-3 border-b border-gray-100 px-2 py-2 text-sm last:border-0 dark:border-dark-700" :data-selected-account="account.id">
              <input :id="`cache-selected-${account.id}`" v-model="removalIds" type="checkbox" :value="account.id" class="checkbox" :disabled="saving" :aria-label="account.name || `#${account.id}`" />
              <label :for="`cache-selected-${account.id}`" class="min-w-0 flex-1 cursor-pointer break-all">{{ account.missing ? t('admin.accounts.cacheReadAdjustment.deletedAccount') : account.name }}</label>
              <span class="shrink-0 text-xs text-gray-500"><span class="hidden sm:inline">{{ account.platform }} </span>#{{ account.id }}</span>
              <button type="button" class="btn btn-secondary btn-sm h-8 w-8 shrink-0 p-0" :title="t('admin.accounts.cacheReadAdjustment.remove')" :aria-label="t('admin.accounts.cacheReadAdjustment.remove')" :disabled="saving" @click="removeAccounts([account.id])"><Icon name="x" size="sm" /></button>
            </div>
            <p v-if="!selectedLoading && !selectedAccounts.length && !selectedError" class="py-6 text-center text-sm text-gray-500">{{ t('admin.accounts.cacheReadAdjustment.noSelectedAccounts') }}</p>
          </div>
        </div>
        <div v-if="selectedPages > 1" class="flex items-center justify-between text-xs text-gray-500">
          <span>{{ selectedPage }} / {{ selectedPages }}</span>
          <div class="flex gap-2">
            <button type="button" class="btn btn-secondary btn-sm" :aria-label="t('admin.accounts.cacheReadAdjustment.previousSelected')" :disabled="selectedPage <= 1 || selectedLoading || saving" @click="changeSelectedPage(-1)"><Icon name="chevronLeft" size="sm" /></button>
            <button type="button" class="btn btn-secondary btn-sm" :aria-label="t('admin.accounts.cacheReadAdjustment.nextSelected')" :disabled="selectedPage >= selectedPages || selectedLoading || saving" @click="changeSelectedPage(1)"><Icon name="chevronRight" size="sm" /></button>
          </div>
        </div>
      </section>

      <section class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-600" :aria-label="t('admin.accounts.cacheReadAdjustment.addAccounts')">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <h4 class="text-sm font-medium">{{ t('admin.accounts.cacheReadAdjustment.addAccounts') }}</h4>
          <button v-if="selectedIds.length" type="button" class="btn btn-secondary btn-sm" :disabled="saving || selectedIds.every(id => selectedSet.has(id))" @click="addAccounts(selectedIds)">
            <Icon name="plus" size="xs" class="mr-1" />{{ t('admin.accounts.cacheReadAdjustment.addSelected') }}
          </button>
        </div>
        <div class="relative">
          <Icon name="search" size="sm" class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
          <input v-model="search" class="input pl-9" type="search" :placeholder="t('admin.accounts.searchAccounts')" :aria-label="t('admin.accounts.searchAccounts')" />
        </div>
        <p v-if="accountError" class="text-sm text-red-600" role="alert">{{ accountError }} <button type="button" class="underline" @click="loadAccounts">{{ t('common.tryAgain') }}</button></p>
        <div class="max-h-48 min-h-12 overflow-y-auto border-y border-gray-200 dark:border-dark-600" :aria-busy="accountsLoading">
          <div v-if="accountsLoading" class="flex h-24 items-center justify-center"><Icon name="refresh" class="animate-spin text-gray-400" /></div>
          <div v-for="account in accountsLoading ? [] : accounts" :key="account.id" class="flex min-h-12 items-center gap-3 border-b border-gray-100 px-2 py-2 text-sm last:border-0 dark:border-dark-700" :data-search-account="account.id">
            <span class="min-w-0 flex-1 break-all">{{ account.name }}</span>
            <span class="shrink-0 text-xs text-gray-500"><span class="hidden sm:inline">{{ account.platform }} </span>#{{ account.id }}</span>
            <button type="button" class="btn btn-secondary btn-sm min-w-20 shrink-0" :disabled="saving || selectedSet.has(account.id)" @click="addAccounts([account.id])">
              <Icon :name="selectedSet.has(account.id) ? 'check' : 'plus'" size="xs" class="mr-1" />{{ t(selectedSet.has(account.id) ? 'admin.accounts.cacheReadAdjustment.added' : 'admin.accounts.cacheReadAdjustment.add') }}
            </button>
          </div>
          <p v-if="!accountsLoading && !accounts.length && !accountError" class="p-4 text-center text-sm text-gray-500">{{ t('common.noData') }}</p>
        </div>
        <div class="flex items-center justify-between text-xs text-gray-500">
          <span>{{ page }} / {{ totalPages }}</span>
          <div class="flex gap-2">
            <button type="button" class="btn btn-secondary btn-sm" :title="t('common.back')" :aria-label="t('common.back')" :disabled="page <= 1 || accountsLoading" @click="page--"><Icon name="chevronLeft" size="sm" /></button>
            <button type="button" class="btn btn-secondary btn-sm" :title="t('common.next')" :aria-label="t('common.next')" :disabled="page >= totalPages || accountsLoading" @click="page++"><Icon name="chevronRight" size="sm" /></button>
          </div>
        </div>
      </section>
    </div>
    <template #footer>
      <div class="flex justify-end">
        <button class="btn btn-secondary" :disabled="saving" @click="close">{{ t('common.close') }}</button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { CacheReadAdjustmentAccount, CacheReadAdjustmentSettings } from '@/api/admin/settings'
import type { Account } from '@/types'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import CacheReadAdjustmentStats from './CacheReadAdjustmentStats.vue'

const props = defineProps<{ show: boolean; selectedIds: number[] }>()
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()
const appStore = useAppStore()
const settings = ref<CacheReadAdjustmentSettings>({ enabled: false, percent: 10, account_ids: [] })
const percent = ref(10)
const loading = ref(false)
const saving = ref(false)
const tab = ref<'settings' | 'stats'>('settings')
const loadError = ref('')
const saveError = ref('')
const accountError = ref('')
const accounts = ref<Account[]>([])
const accountsLoading = ref(false)
const search = ref('')
const page = ref(1)
const totalPages = ref(1)
const selectedAccounts = ref<CacheReadAdjustmentAccount[]>([])
const selectedLoading = ref(false)
const selectedError = ref('')
const selectedPage = ref(1)
const selectedPages = ref(1)
const removalIds = ref<number[]>([])
const selectedSet = computed(() => new Set(settings.value.account_ids))
const allSelected = computed(() => selectedAccounts.value.length > 0 && selectedAccounts.value.every(account => removalIds.value.includes(account.id)))
const someSelected = computed(() => selectedAccounts.value.some(account => removalIds.value.includes(account.id)))
const validPercent = computed(() => Number.isInteger(percent.value) && percent.value >= 0 && percent.value <= 100)
let controller: AbortController | undefined
let selectedController: AbortController | undefined
let searchTimer: ReturnType<typeof setTimeout> | undefined
let loadVersion = 0

function close() {
  if (!saving.value) emit('close')
}

function selectPage(checked: boolean) {
  const ids = new Set(selectedAccounts.value.map(account => account.id))
  removalIds.value = checked
    ? [...new Set([...removalIds.value, ...ids])]
    : removalIds.value.filter(id => !ids.has(id))
}

function changeSelectedPage(delta: number) {
  selectedPage.value += delta
  void loadSelected()
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

async function loadSelected() {
  selectedController?.abort()
  const request = new AbortController()
  selectedController = request
  selectedLoading.value = true
  selectedError.value = ''
  try {
    const result = await adminAPI.settings.getCacheReadAdjustmentAccounts(selectedPage.value, request.signal)
    if (request.signal.aborted) return
    selectedAccounts.value = result.items
    selectedPages.value = Math.max(1, result.pages)
    if (selectedPage.value !== result.page) selectedPage.value = result.page
  } catch (error) {
    if (!request.signal.aborted) selectedError.value = extractApiErrorMessage(error, t('admin.accounts.cacheReadAdjustment.loadFailed'))
  } finally {
    if (!request.signal.aborted) selectedLoading.value = false
  }
}

async function load() {
  const version = ++loadVersion
  loading.value = true
  loadError.value = ''
  saveError.value = ''
  try {
    const result = await adminAPI.settings.getCacheReadAdjustmentSettings()
    if (version !== loadVersion) return
    settings.value = result
    removalIds.value = []
    percent.value = result.percent
    await Promise.all([loadAccounts(), loadSelected()])
  } catch (error) {
    if (version === loadVersion) loadError.value = extractApiErrorMessage(error, t('admin.accounts.cacheReadAdjustment.loadFailed'))
  } finally {
    if (version === loadVersion) loading.value = false
  }
}

async function mutate(update: () => Promise<CacheReadAdjustmentSettings>, refreshAccounts = false) {
  if (saving.value || loading.value || loadError.value) return false
  saving.value = true
  saveError.value = ''
  try {
    settings.value = await update()
    removalIds.value = removalIds.value.filter(id => selectedSet.value.has(id))
    if (refreshAccounts) await loadSelected()
    return true
  } catch (error) {
    saveError.value = extractApiErrorMessage(error, t('admin.accounts.cacheReadAdjustment.saveFailed'))
    return false
  } finally {
    saving.value = false
  }
}

async function setEnabled(enabled: boolean) {
  if (await mutate(() => adminAPI.settings.patchCacheReadAdjustmentSettings({ enabled }))) {
    appStore.showSuccess(t(settings.value.enabled ? 'admin.accounts.cacheReadAdjustment.active' : 'admin.accounts.cacheReadAdjustment.inactive'))
  }
}

async function savePercent() {
  if (!validPercent.value) return
  const value = percent.value
  if (await mutate(() => adminAPI.settings.patchCacheReadAdjustmentSettings({ percent: value }))) {
    percent.value = settings.value.percent
    appStore.showSuccess(t('admin.accounts.cacheReadAdjustment.saved'))
  }
}

async function addAccounts(ids: number[]) {
  const additions = [...new Set(ids)].filter(id => !selectedSet.value.has(id))
  if (additions.length) await mutate(() => adminAPI.settings.addCacheReadAdjustmentAccounts(additions), true)
}

async function removeAccounts(ids: number[]) {
  if (ids.length) await mutate(() => adminAPI.settings.removeCacheReadAdjustmentAccounts([...ids]), true)
}

watch(() => props.show, show => {
  if (show) {
    tab.value = 'settings'
    search.value = ''
    page.value = 1
    selectedPage.value = 1
    void load()
  } else {
    loadVersion++
    controller?.abort()
    selectedController?.abort()
    clearTimeout(searchTimer)
  }
}, { immediate: true })
watch(search, () => {
  clearTimeout(searchTimer)
  controller?.abort()
  accounts.value = []
  accountsLoading.value = true
  searchTimer = setTimeout(() => {
    if (!props.show) return
    if (page.value !== 1) page.value = 1
    else void loadAccounts()
  }, 250)
})
watch(page, () => { if (props.show && !loading.value) void loadAccounts() })
onBeforeUnmount(() => { controller?.abort(); selectedController?.abort(); clearTimeout(searchTimer) })
</script>
