<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div>
          <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ t('admin.accessBlocks.title') }}</h1>
          <p class="mt-1 max-w-3xl text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.description') }}</p>
        </div>
        <button type="button" class="btn btn-secondary inline-flex items-center gap-2" :disabled="loading || saving" @click="loadAll">
          <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
          {{ t('admin.accessBlocks.refresh') }}
        </button>
      </div>

      <div v-if="loading" class="flex items-center justify-center py-16">
        <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
      </div>

      <template v-else>
        <div
          v-if="!form.enabled"
          class="border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-900 dark:border-amber-800 dark:bg-amber-950/30 dark:text-amber-200"
        >
          {{ t('admin.accessBlocks.featureDisabled') }}
          <router-link to="/admin/settings#features" class="ml-1 font-medium underline">
            {{ t('admin.accessBlocks.openFeatureSettings') }}
          </router-link>
        </div>

        <section class="card">
          <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.accessBlocks.loginProtection') }}</h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.loginProtectionHint') }}</p>
          </div>
          <div class="space-y-5 p-6">
            <div class="flex items-center justify-between gap-4">
              <span class="font-medium text-gray-900 dark:text-white">{{ t('admin.accessBlocks.loginProtection') }}</span>
              <Toggle v-model="form.login_protection_enabled" />
            </div>
            <div v-if="form.login_protection_enabled" class="grid grid-cols-1 gap-5 border-t border-gray-100 pt-5 dark:border-dark-700 sm:grid-cols-3">
              <label class="block">
                <span class="input-label">{{ t('admin.accessBlocks.failureThreshold') }}</span>
                <div class="flex items-center gap-2">
                  <input v-model.number="form.login_failure_threshold" type="number" min="1" max="1000" class="input" />
                  <span class="shrink-0 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.attempts') }}</span>
                </div>
              </label>
              <label class="block">
                <span class="input-label">{{ t('admin.accessBlocks.failureWindow') }}</span>
                <div class="flex items-center gap-2">
                  <input v-model.number="form.login_failure_window_seconds" type="number" min="10" max="86400" class="input" />
                  <span class="shrink-0 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.seconds') }}</span>
                </div>
              </label>
              <label class="block">
                <span class="input-label">{{ t('admin.accessBlocks.temporaryDuration') }}</span>
                <div class="flex items-center gap-2">
                  <input v-model.number="form.login_temporary_block_seconds" type="number" min="10" max="604800" class="input" />
                  <span class="shrink-0 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.seconds') }}</span>
                </div>
              </label>
            </div>
          </div>
        </section>

        <section class="card">
          <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.accessBlocks.headers') }}</h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.headersHint') }}</p>
          </div>
          <div class="space-y-3 p-6">
            <div v-for="(rule, index) in form.blocked_headers" :key="`${index}-${rule.name}`" class="grid grid-cols-1 items-center gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,2fr)_auto]">
              <input v-model.trim="rule.name" type="text" class="input" :placeholder="t('admin.accessBlocks.headerName')" maxlength="64" />
              <input v-model.trim="rule.value" type="text" class="input" :placeholder="t('admin.accessBlocks.headerValue')" maxlength="256" />
              <button type="button" class="btn btn-secondary inline-flex h-10 w-10 items-center justify-center justify-self-end p-0 text-red-600 dark:text-red-400" :title="t('admin.accessBlocks.removeHeader')" @click="removeHeader(index)">
                <Icon name="trash" size="sm" />
              </button>
            </div>
            <button type="button" class="btn btn-secondary inline-flex items-center gap-2" :disabled="form.blocked_headers.length >= 20" @click="addHeader">
              <Icon name="plus" size="sm" />
              {{ t('admin.accessBlocks.addHeader') }}
            </button>
          </div>
        </section>

        <section class="card">
          <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.accessBlocks.panelBlacklist') }}</h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.panelBlacklistHint') }}</p>
          </div>
          <div class="space-y-5 p-6">
            <div class="flex items-center justify-between gap-4">
              <span class="font-medium text-gray-900 dark:text-white">{{ t('admin.accessBlocks.panelBlacklist') }}</span>
              <Toggle v-model="form.panel_blacklist_enabled" />
            </div>
            <div v-if="form.panel_blacklist_enabled" class="grid grid-cols-1 gap-5 border-t border-gray-100 pt-5 dark:border-dark-700 sm:grid-cols-2">
              <label class="block">
                <span class="input-label">{{ t('admin.accessBlocks.blacklistThreshold') }}</span>
                <div class="flex items-center gap-2">
                  <input v-model.number="form.panel_blacklist_threshold" type="number" min="1" max="1000" class="input" />
                  <span class="shrink-0 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.attempts') }}</span>
                </div>
              </label>
              <label class="block">
                <span class="input-label">{{ t('admin.accessBlocks.blacklistWindow') }}</span>
                <div class="flex items-center gap-2">
                  <input v-model.number="form.panel_blacklist_window_seconds" type="number" min="10" max="86400" class="input" />
                  <span class="shrink-0 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.seconds') }}</span>
                </div>
              </label>
            </div>
          </div>
        </section>

        <div class="flex justify-end">
          <button type="button" class="btn btn-primary inline-flex items-center gap-2" :disabled="saving || !settingsLoaded" @click="saveSettings">
            <Icon name="check" size="sm" />
            {{ saving ? t('common.loading') : t('admin.accessBlocks.save') }}
          </button>
        </div>

        <section class="card">
          <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
            <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.accessBlocks.manual') }}</h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.manualHint') }}</p>
          </div>
          <div class="grid grid-cols-1 gap-4 p-6 md:grid-cols-[minmax(0,1fr)_auto_auto_auto] md:items-end">
            <label class="block">
              <span class="input-label">{{ t('admin.accessBlocks.ip') }}</span>
              <input v-model.trim="manual.ip" type="text" class="input font-mono" placeholder="203.0.113.10" autocomplete="off" />
            </label>
            <label class="block">
              <span class="input-label">{{ t('admin.accessBlocks.status') }}</span>
              <select v-model="manual.permanent" class="input">
                <option :value="false">{{ t('admin.accessBlocks.temporary') }}</option>
                <option :value="true">{{ t('admin.accessBlocks.permanent') }}</option>
              </select>
            </label>
            <label v-if="!manual.permanent" class="block">
              <span class="input-label">{{ t('admin.accessBlocks.duration') }}</span>
              <div class="flex items-center gap-2">
                <input v-model.number="manual.duration_seconds" type="number" min="10" max="604800" class="input w-28" />
                <span class="shrink-0 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.seconds') }}</span>
              </div>
            </label>
            <button type="button" class="btn btn-secondary inline-flex items-center justify-center gap-2" :disabled="adding" @click="addBlock">
              <Icon name="plus" size="sm" />
              {{ adding ? t('common.loading') : t('admin.accessBlocks.add') }}
            </button>
          </div>
        </section>

        <section class="card">
          <div class="flex flex-col gap-2 border-b border-gray-100 px-6 py-4 dark:border-dark-700 sm:flex-row sm:items-center sm:justify-between">
            <div>
              <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.accessBlocks.active') }}</h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.activeHint') }}</p>
            </div>
            <span class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.page', { page, total }) }}</span>
          </div>
          <div class="max-h-[440px] overflow-y-auto">
            <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-700">
              <thead class="sticky top-0 bg-gray-50 dark:bg-dark-800">
                <tr>
                  <th class="px-6 py-3 text-left font-medium text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.ip') }}</th>
                  <th class="px-6 py-3 text-left font-medium text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.status') }}</th>
                  <th class="px-6 py-3 text-left font-medium text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.source') }}</th>
                  <th class="px-6 py-3 text-left font-medium text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.remaining') }}</th>
                  <th class="px-6 py-3 text-right font-medium text-gray-500 dark:text-gray-400"></th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                <tr v-for="block in blocks" :key="`${block.ip}-${block.permanent}`">
                  <td class="whitespace-nowrap px-6 py-3 font-mono text-gray-900 dark:text-white">{{ block.ip }}</td>
                  <td class="whitespace-nowrap px-6 py-3 text-gray-600 dark:text-gray-300">{{ block.permanent ? t('admin.accessBlocks.permanentStatus') : t('admin.accessBlocks.temporaryStatus') }}</td>
                  <td class="whitespace-nowrap px-6 py-3 text-gray-600 dark:text-gray-300">{{ sourceLabel(block.source) }}</td>
                  <td class="whitespace-nowrap px-6 py-3 text-gray-600 dark:text-gray-300">{{ block.permanent ? '-' : formatRemaining(block.remaining_seconds) }}</td>
                  <td class="px-6 py-3 text-right">
                    <button type="button" class="btn btn-secondary btn-sm text-red-600 dark:text-red-400" :disabled="removing === block.ip" @click="removeBlock(block.ip)">
                      {{ removing === block.ip ? t('common.loading') : t('admin.accessBlocks.unblock') }}
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
            <p v-if="blocksLoaded && blocks.length === 0" class="px-6 py-12 text-center text-sm text-gray-500 dark:text-gray-400">{{ t('admin.accessBlocks.empty') }}</p>
            <p v-else-if="!blocksLoaded" class="px-6 py-12 text-center text-sm text-amber-700 dark:text-amber-300">{{ t('admin.accessBlocks.listUnavailable') }}</p>
          </div>
          <div v-if="total > pageSize" class="flex items-center justify-between border-t border-gray-100 px-6 py-3 dark:border-dark-700">
            <button type="button" class="btn btn-secondary btn-sm" :disabled="page <= 1" @click="changePage(page - 1)">{{ t('admin.accessBlocks.previous') }}</button>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="page * pageSize >= total" @click="changePage(page + 1)">{{ t('admin.accessBlocks.next') }}</button>
          </div>
        </section>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api'
import type { AccessBlock, AccessBlockSettings } from '@/api/admin/accessBlocks'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import Toggle from '@/components/common/Toggle.vue'
import { useAppStore } from '@/stores'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(true)
const settingsLoaded = ref(false)
const blocksLoaded = ref(false)
const saving = ref(false)
const adding = ref(false)
const removing = ref('')
const blocks = ref<AccessBlock[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20

const form = reactive<AccessBlockSettings>({
  enabled: true,
  login_protection_enabled: true,
  login_failure_threshold: 10,
  login_failure_window_seconds: 600,
  login_temporary_block_seconds: 3600,
  blocked_headers: [],
  panel_blacklist_enabled: false,
  panel_blacklist_threshold: 10,
  panel_blacklist_window_seconds: 600,
})

const manual = reactive({ ip: '', permanent: false, duration_seconds: 3600 })

function formatRemaining(seconds: number): string {
  const value = Math.max(0, Math.ceil(Number(seconds) || 0))
  const minutes = Math.floor(value / 60)
  const remainder = value % 60
  return minutes > 0
    ? t('admin.accessBlocks.remainingMinutes', { minutes, seconds: remainder })
    : t('admin.accessBlocks.remainingSeconds', { seconds: remainder })
}

function sourceLabel(source: string): string {
  const key = source === 'login_failures' ? 'sourceLogin' : source === 'panel_rate_limit' ? 'sourcePanel' : 'sourceManual'
  return t(`admin.accessBlocks.${key}`)
}

async function loadAll() {
  loading.value = true
  const [settingsResult, blocksResult] = await Promise.allSettled([
    adminAPI.accessBlocks.getSettings(),
    adminAPI.accessBlocks.list({ page: page.value, page_size: pageSize }),
  ])
  if (settingsResult.status === 'fulfilled') {
    const settings = settingsResult.value
    Object.assign(form, settings, { blocked_headers: settings.blocked_headers ?? [] })
    settingsLoaded.value = true
  } else {
    settingsLoaded.value = false
    appStore.showError(extractApiErrorMessage(settingsResult.reason, t('admin.accessBlocks.policyLoadFailed')))
  }
  if (blocksResult.status === 'fulfilled') {
    const result = blocksResult.value
    blocks.value = result.items ?? []
    total.value = result.total ?? blocks.value.length
    blocksLoaded.value = true
  } else {
    blocksLoaded.value = false
    appStore.showError(extractApiErrorMessage(blocksResult.reason, t('admin.accessBlocks.listLoadFailed')))
  }
  loading.value = false
}

function addHeader() {
  if (form.blocked_headers.length < 20) form.blocked_headers.push({ name: '', value: '' })
}

function removeHeader(index: number) {
  form.blocked_headers.splice(index, 1)
}

async function saveSettings() {
  saving.value = true
  try {
    const updated = await adminAPI.accessBlocks.updateSettings({
      login_protection_enabled: form.login_protection_enabled,
      login_failure_threshold: form.login_failure_threshold,
      login_failure_window_seconds: form.login_failure_window_seconds,
      login_temporary_block_seconds: form.login_temporary_block_seconds,
      blocked_headers: form.blocked_headers.filter(rule => rule.name.trim() && rule.value.trim()),
      panel_blacklist_enabled: form.panel_blacklist_enabled,
      panel_blacklist_threshold: form.panel_blacklist_threshold,
      panel_blacklist_window_seconds: form.panel_blacklist_window_seconds,
    })
    Object.assign(form, updated, { blocked_headers: updated.blocked_headers ?? [] })
    appStore.showSuccess(t('admin.accessBlocks.saved'))
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('admin.accessBlocks.saveFailed')))
  } finally {
    saving.value = false
  }
}

async function addBlock() {
  if (!manual.ip.trim()) return
  adding.value = true
  try {
    await adminAPI.accessBlocks.add({ ip: manual.ip.trim(), permanent: manual.permanent, duration_seconds: manual.permanent ? 0 : manual.duration_seconds })
    manual.ip = ''
    appStore.showSuccess(t('admin.accessBlocks.addSuccess'))
    try {
      await loadBlocks()
    } catch (error: unknown) {
      appStore.showError(extractApiErrorMessage(error, t('admin.accessBlocks.listLoadFailed')))
    }
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('admin.accessBlocks.addFailed')))
  } finally {
    adding.value = false
  }
}

async function loadBlocks() {
  try {
    const result = await adminAPI.accessBlocks.list({ page: page.value, page_size: pageSize })
    blocks.value = result.items ?? []
    total.value = result.total ?? blocks.value.length
    blocksLoaded.value = true
  } catch (error: unknown) {
    blocksLoaded.value = false
    throw error
  }
}

async function removeBlock(ip: string) {
  if (!window.confirm(t('admin.accessBlocks.unblockConfirm', { ip }))) return
  removing.value = ip
  try {
    await adminAPI.accessBlocks.remove(ip)
    appStore.showSuccess(t('admin.accessBlocks.unblocked'))
    try {
      await loadBlocks()
    } catch (error: unknown) {
      appStore.showError(extractApiErrorMessage(error, t('admin.accessBlocks.listLoadFailed')))
    }
  } catch (error: unknown) {
    appStore.showError(extractApiErrorMessage(error, t('admin.accessBlocks.unblockFailed')))
  } finally {
    removing.value = ''
  }
}

async function changePage(nextPage: number) {
  const previousPage = page.value
  page.value = nextPage
  try {
    await loadBlocks()
  } catch (error: unknown) {
    page.value = previousPage
    appStore.showError(extractApiErrorMessage(error, t('admin.accessBlocks.listLoadFailed')))
  }
}

onMounted(loadAll)
</script>
