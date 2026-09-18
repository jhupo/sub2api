<template>
  <AppLayout>
    <div class="space-y-5 pb-12">
      <section class="relative z-30 glass-card overflow-visible p-0" data-testid="state-toolbar">
        <header class="flex flex-wrap items-start justify-between gap-4 border-b border-gray-100 px-5 py-4 dark:border-dark-700 sm:px-6">
          <div class="min-w-0">
            <h1 class="page-title flex items-center gap-2 text-xl font-black text-gray-900 dark:text-white">
              <span class="grid h-8 w-8 place-items-center rounded-xl bg-primary-50 text-primary-500 dark:bg-primary-900/30 dark:text-primary-300">
                <Icon name="chart" size="sm" />
              </span>
              {{ t(`${key}.poolTitle`) }}
            </h1>
            <p class="mt-1.5 max-w-4xl text-xs leading-5 text-gray-500 dark:text-gray-400">
              {{ t(`${key}.poolHint`, { length: settings.expected_length, ttl: settings.ttl_minutes }) }}
            </p>
          </div>
          <span class="rounded-full px-2.5 py-1 text-xs font-semibold" :class="settings.auto_replace_enabled ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'">
            {{ t(`${key}.${settings.auto_replace_enabled ? 'autoReplaceOn' : 'autoReplaceOff'}`) }}
          </span>
        </header>

        <div class="flex flex-col gap-2 px-4 py-3 sm:px-5 lg:flex-row lg:items-center">
          <div class="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap">
            <StateMultiSelectFilter
              v-model="selectedAccounts"
              :options="accountOptions"
              :placeholder="t(`${key}.filterAccounts`)"
              :selected-label="t(`${key}.selectedAccounts`)"
              :clear-label="t(`${key}.filterAll`)"
              :all-label="t(`${key}.selectAll`)"
              test-id="state-account-filter"
            />
            <StateMultiSelectFilter
              v-model="selectedModels"
              align="right"
              :options="modelOptions"
              :placeholder="t(`${key}.filterModels`)"
              :selected-label="t(`${key}.selectedModels`)"
              :clear-label="t(`${key}.filterAll`)"
              :all-label="t(`${key}.selectAll`)"
              test-id="state-model-filter"
            />
          </div>
          <div class="flex min-w-0 flex-1 gap-2 lg:justify-end">
            <label class="flex shrink-0 cursor-pointer items-center gap-2 text-xs text-gray-600 dark:text-gray-300">
              <input v-model="onlyEnabled" data-testid="state-only-enabled" type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-900" />
              {{ t(`${key}.onlyEnabled`) }}
            </label>
            <div class="relative min-w-0 flex-1 lg:max-w-80">
              <Icon name="search" size="sm" class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
              <input v-model="search" type="search" class="input h-10 pl-9" :placeholder="t(`${key}.search`)" :aria-label="t(`${key}.search`)" />
            </div>
            <button type="button" data-testid="state-list-refresh" class="btn btn-secondary btn-icon h-10 w-10 shrink-0 rounded-xl" :disabled="loading || busy" :title="t(`${key}.refresh`)" @click="loadMatrix()">
              <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
            </button>
          </div>
        </div>
      </section>

      <p v-if="error" role="alert" class="rounded-2xl border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300">{{ error }}</p>

      <div v-if="loading && !loaded" class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
        <div v-for="i in 8" :key="i" class="h-80 animate-pulse rounded-[24px] bg-white/60 dark:bg-dark-800" />
      </div>
      <div v-else-if="!visibleRows.length" class="glass-card py-16 text-center text-sm text-gray-500 dark:text-gray-400">{{ t(`${key}.${onlyEnabled ? 'noEnabledPairs' : 'noAccounts'}`) }}</div>
      <div v-else class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4" data-testid="state-cards">
        <article
          v-for="row in visibleRows"
          :key="pairKey(row)"
          class="group relative z-0 glass-card flex min-h-[326px] flex-col overflow-visible rounded-[24px] p-5 text-left hover:z-20"
        >
          <header class="flex items-start gap-3">
            <span class="grid h-9 w-9 shrink-0 place-items-center rounded-xl bg-gradient-to-br from-emerald-400 to-cyan-600 text-xs font-black text-white shadow-sm">
              {{ accountInitial(row.account_name) }}
            </span>
            <div class="min-w-0 flex-1">
              <div class="truncate text-base font-semibold text-gray-900 dark:text-gray-100" :title="row.account_name">{{ row.account_name }}</div>
              <div class="mt-1 flex min-w-0 flex-wrap items-center gap-1.5">
                <span class="rounded-md bg-gray-100 px-1.5 py-0.5 font-mono text-[10px] font-medium text-gray-600 dark:bg-dark-700 dark:text-gray-300">#{{ row.account_id }}</span>
                <span class="max-w-full truncate rounded-md bg-primary-50 px-1.5 py-0.5 font-mono text-[10px] font-medium text-primary-700 dark:bg-primary-950/40 dark:text-primary-300" :title="row.model">{{ row.model }}</span>
              </div>
            </div>
            <span class="shrink-0 rounded-full px-2.5 py-1 text-xs font-semibold" :class="statusClass(row)">{{ t(`${key}.status.${statusKey(row)}`) }}</span>
          </header>

          <div class="mt-5 grid grid-cols-3 gap-2">
            <div class="rounded-2xl border border-slate-200/80 bg-slate-50/85 p-3 dark:border-dark-700/50 dark:bg-dark-900/40">
              <div class="text-[10px] font-semibold uppercase tracking-wider text-gray-400">{{ t(`${key}.stateLength`) }}</div>
              <div class="mt-1.5 font-mono text-lg font-bold tabular-nums" :class="row.cached && row.upstream_expires_at > now ? 'text-emerald-600 dark:text-emerald-400' : 'text-gray-900 dark:text-gray-100'">{{ row.cached ? row.state_length : row.observed_length }}</div>
            </div>
            <div class="rounded-2xl border border-slate-200/80 bg-slate-50/85 p-3 dark:border-dark-700/50 dark:bg-dark-900/40">
              <div class="text-[10px] font-semibold uppercase tracking-wider text-gray-400">{{ t(`${key}.remaining`) }}</div>
              <div class="mt-1.5 font-mono text-base font-bold tabular-nums" :class="row.upstream_expires_at > now ? 'text-gray-900 dark:text-gray-100' : 'text-gray-400'">{{ row.upstream_expires_at ? remaining(row.upstream_expires_at) : '—' }}</div>
            </div>
            <div class="rounded-2xl border border-slate-200/80 bg-slate-50/85 p-3 dark:border-dark-700/50 dark:bg-dark-900/40">
              <div class="text-[10px] font-semibold uppercase tracking-wider text-gray-400">{{ t(`${key}.nextRotation`) }}</div>
              <div class="mt-1.5 font-mono text-base font-bold tabular-nums text-gray-900 dark:text-gray-100">{{ row.rotation_at ? remaining(row.rotation_at) : '—' }}</div>
            </div>
          </div>

          <div class="mt-4 rounded-2xl border border-slate-200/80 bg-white/70 p-3 dark:border-dark-700/50 dark:bg-dark-900/20">
            <div class="flex items-center justify-between gap-2 text-[11px] text-gray-500 dark:text-gray-400">
              <span>X-Codex-Turn-State</span>
              <span v-if="row.expiry_source" class="rounded-md bg-gray-100 px-1.5 py-0.5 dark:bg-dark-700">{{ t(`${key}.expirySource.${row.expiry_source}`) }}</span>
            </div>
            <p class="mt-2 truncate font-mono text-xs font-medium text-gray-700 dark:text-gray-300" :title="row.digest">{{ row.digest ? `${row.digest}…` : t(`${key}.stateUnavailable`) }}</p>
            <div class="mt-2 h-1.5 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-600">
              <div class="h-full rounded-full transition-all" :class="progressClass(row)" :style="{ width: `${progress(row)}%` }"></div>
            </div>
            <div v-if="row.last_error" class="mt-2 text-[11px] text-red-600 dark:text-red-400">
              <p>{{ t(`${key}.lastRefreshError`) }}<span v-if="row.last_refresh_at"> · {{ new Date(row.last_refresh_at).toLocaleString(locale) }}</span></p>
              <p class="line-clamp-2" :title="row.last_error">{{ row.last_error }}</p>
            </div>
          </div>

          <div class="mt-auto flex items-end justify-between gap-3 pt-4">
            <div class="flex min-w-0 flex-wrap gap-2">
              <button type="button" class="btn btn-secondary inline-flex items-center gap-1.5 px-3 py-2 text-xs" :disabled="busy || !row.enabled" @click="openSetState(row)">
                <Icon name="edit" size="xs" />
                {{ t(`${key}.setState`) }}
              </button>
              <button type="button" class="btn btn-primary inline-flex items-center gap-1.5 px-3 py-2 text-xs" :disabled="busy || !row.enabled" @click="refreshState(row)">
                <Icon name="refresh" size="xs" :class="actionKey === pairKey(row) ? 'animate-spin' : ''" />
                {{ t(`${key}.refreshNow`) }}
              </button>
            </div>
            <label class="flex shrink-0 flex-col items-end gap-1 text-[10px] font-medium text-gray-500 dark:text-gray-400">
              {{ t(`${key}.pairEnabled`) }}
              <Toggle :model-value="row.enabled" :aria-label="`${row.account_name} / ${row.model}`" :disabled="busy || !settings.enabled" @update:model-value="setPair(row, $event)" />
            </label>
          </div>
        </article>
      </div>

      <div v-if="filteredRows.length > pageSize" class="flex items-center justify-between text-xs text-gray-500 dark:text-gray-400">
        <span>{{ t(`${key}.page`, { page, total: totalPages }) }}</span>
        <div class="flex gap-2">
          <button type="button" class="btn btn-secondary text-xs" :disabled="page <= 1" @click="page--">{{ t(`${key}.previous`) }}</button>
          <button type="button" class="btn btn-secondary text-xs" :disabled="page >= totalPages" @click="page++">{{ t(`${key}.next`) }}</button>
        </div>
      </div>
    </div>

    <BaseDialog :show="Boolean(editingRow)" :title="t(`${key}.setStateTitle`)" width="normal" @close="closeSetState">
      <div v-if="editingRow" class="space-y-4">
        <div class="rounded-2xl bg-gray-50 p-3 text-sm dark:bg-dark-800">
          <p class="font-medium text-gray-900 dark:text-white">{{ editingRow.account_name }}</p>
          <p class="mt-1 truncate font-mono text-xs text-gray-500 dark:text-gray-400">{{ editingRow.model }} · #{{ editingRow.account_id }}</p>
        </div>
        <label class="block">
          <span class="input-label">X-Codex-Turn-State</span>
          <textarea v-model.trim="stateDraft" data-testid="state-input" rows="6" class="input resize-y font-mono text-xs" :placeholder="t(`${key}.statePlaceholder`)" :disabled="busy"></textarea>
          <span class="mt-1 flex justify-between gap-3 text-xs text-gray-500 dark:text-gray-400">
            <span>{{ t(`${key}.stateInputHint`, { length: settings.expected_length }) }}</span>
            <span class="tabular-nums" :class="stateDraft.length === settings.expected_length ? 'text-green-600 dark:text-green-400' : ''">{{ stateDraft.length }} B</span>
          </span>
        </label>
      </div>
      <template #footer>
        <button type="button" class="btn btn-secondary" :disabled="busy" @click="closeSetState">{{ t('common.cancel') }}</button>
        <button type="button" data-testid="state-submit" class="btn btn-primary" :disabled="busy || stateDraft.length !== settings.expected_length" @click="setState">{{ t(`${key}.setState`) }}</button>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import StateMultiSelectFilter from './components/StateMultiSelectFilter.vue'
import { useAppStore } from '@/stores'
import { extractApiErrorMessage } from '@/utils/apiError'
import { upstreamStateApi, type UpstreamStateSettings, type UpstreamStateMatrixRow } from '@/api/admin/upstreamState'

const key = 'admin.settings.upstreamState'
const { t, locale } = useI18n()
const app = useAppStore()
const settings = reactive<UpstreamStateSettings>({
  enabled: true,
  auto_replace_enabled: true,
  ttl_minutes: 40,
  expected_length: 292,
  webshare_enabled: false,
  webshare_api_key_configured: false,
  webshare_country_mode: 'random',
  webshare_countries: [],
  revision: '',
  state_revision: '',
  pairs: []
})
const rows = ref<UpstreamStateMatrixRow[]>([])
const search = ref('')
const filterStorageKey = 'sub2api:upstream-state:filters'
interface StateFilters { onlyEnabled: boolean; accounts: string[]; models: string[] }
function readFilters(): StateFilters {
  try {
    const saved = JSON.parse(localStorage.getItem(filterStorageKey) || 'null')
    if (saved && typeof saved.onlyEnabled === 'boolean' &&
      Array.isArray(saved.accounts) && saved.accounts.every((v: unknown) => typeof v === 'string') &&
      Array.isArray(saved.models) && saved.models.every((v: unknown) => typeof v === 'string')) return saved
  } catch { /* Storage may be unavailable; enabled pairs are the default. */ }
  return { onlyEnabled: true, accounts: [], models: [] }
}
const savedFilters = readFilters()
const onlyEnabled = ref(savedFilters.onlyEnabled)
const selectedAccounts = ref<string[]>(savedFilters.accounts)
const selectedModels = ref<string[]>(savedFilters.models)
const page = ref(1)
const pageSize = 12
const now = ref(Date.now())
const loading = ref(false)
const loaded = ref(false)
const busy = ref(false)
const actionKey = ref('')
const error = ref('')
const editingRow = ref<UpstreamStateMatrixRow | null>(null)
const stateDraft = ref('')
let timer: ReturnType<typeof setInterval> | undefined

const selectableRows = computed(() => onlyEnabled.value ? rows.value.filter(row => row.enabled) : rows.value)
const accountOptions = computed(() => {
  const accounts = new Map<number, string>()
  for (const row of selectableRows.value) accounts.set(row.account_id, row.account_name)
  return [...accounts.entries()]
    .sort(([a], [b]) => a - b)
    .map(([id, name]) => ({ value: String(id), label: name, description: `#${id}` }))
})
const modelOptions = computed(() => [...new Set(selectableRows.value.map(row => row.model))]
  .sort((a, b) => a.localeCompare(b))
  .map(model => ({ value: model, label: model })))
const filteredRows = computed(() => {
  const term = search.value.trim().toLowerCase()
  return selectableRows.value.filter(row => {
    if (selectedAccounts.value.length && !selectedAccounts.value.includes(String(row.account_id))) return false
    if (selectedModels.value.length && !selectedModels.value.includes(row.model)) return false
    return !term || `${row.account_id} ${row.account_name} ${row.model}`.toLowerCase().includes(term)
  })
})
const totalPages = computed(() => Math.max(1, Math.ceil(filteredRows.value.length / pageSize)))
const visibleRows = computed(() => filteredRows.value.slice((page.value - 1) * pageSize, page.value * pageSize))

watch([search, onlyEnabled, selectedAccounts, selectedModels], () => { page.value = 1 }, { deep: true })
watch([onlyEnabled, selectedAccounts, selectedModels], () => {
  try {
    localStorage.setItem(filterStorageKey, JSON.stringify({
      onlyEnabled: onlyEnabled.value, accounts: selectedAccounts.value, models: selectedModels.value
    }))
  } catch { /* Keep filters usable when browser storage is unavailable. */ }
}, { deep: true })
watch(totalPages, value => { if (page.value > value) page.value = value })

function pairKey(row: UpstreamStateMatrixRow) { return `${row.account_id}:${row.model}` }
function accountInitial(name: string) { return (name.trim()[0] || '?').toUpperCase() }
function remaining(value: number) {
  const seconds = Math.max(0, Math.ceil((value - now.value) / 1000))
  if (!seconds) return t(`${key}.expired`)
  const minutes = Math.floor(seconds / 60)
  if (minutes >= 60) return new Intl.NumberFormat(locale.value || undefined, { maximumFractionDigits: 1 }).format(minutes / 60) + 'h'
  return `${minutes}:${String(seconds % 60).padStart(2, '0')}`
}
function progress(row: UpstreamStateMatrixRow) {
  const start = row.issued_at || row.acquired_at
  if (!start || !row.upstream_expires_at || row.upstream_expires_at <= start) return 0
  return Math.max(0, Math.min(100, ((row.upstream_expires_at - now.value) / (row.upstream_expires_at - start)) * 100))
}
function statusKey(row: UpstreamStateMatrixRow) {
  if (!row.enabled) return 'disabled'
  if (row.cached && row.upstream_expires_at > now.value) return 'normal'
  if (row.upstream_expires_at && row.upstream_expires_at <= now.value) return 'expired'
  return row.validation || 'waiting'
}
function statusClass(row: UpstreamStateMatrixRow) {
  const status = statusKey(row)
  if (status === 'normal') return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300'
  if (status === 'waiting' || status === 'disabled') return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
  return 'bg-red-50 text-red-700 dark:bg-red-950/40 dark:text-red-300'
}
function progressClass(row: UpstreamStateMatrixRow) { return statusKey(row) === 'normal' ? 'bg-emerald-500' : 'bg-red-500' }

async function loadSettings() {
  Object.assign(settings, await upstreamStateApi.settings())
}
async function loadMatrix(preserveError = false) {
  if (loading.value) return
  loading.value = true
  try {
    rows.value = await upstreamStateApi.matrix()
    const accounts = new Set(rows.value.map(row => String(row.account_id)))
    const models = new Set(rows.value.map(row => row.model))
    const retainedAccounts = selectedAccounts.value.filter(id => accounts.has(id))
    const retainedModels = selectedModels.value.filter(model => models.has(model))
    if (retainedAccounts.length !== selectedAccounts.value.length) selectedAccounts.value = retainedAccounts
    if (retainedModels.length !== selectedModels.value.length) selectedModels.value = retainedModels
    if (!preserveError) error.value = ''
    loaded.value = true
  } catch (err: unknown) {
    error.value = extractApiErrorMessage(err, t(`${key}.listFailed`))
  } finally {
    loading.value = false
  }
}
async function setPair(row: UpstreamStateMatrixRow, enabled: boolean) {
  if (busy.value) return
  busy.value = true
  actionKey.value = pairKey(row)
  try {
    const saved = await upstreamStateApi.setPair(row, enabled, settings.revision)
    Object.assign(settings, saved)
    row.enabled = enabled
    await loadMatrix()
  } catch (err: unknown) {
    error.value = extractApiErrorMessage(err, t(`${key}.saveFailed`))
  } finally {
    busy.value = false
    actionKey.value = ''
  }
}
function openSetState(row: UpstreamStateMatrixRow) {
  editingRow.value = row
  stateDraft.value = ''
}
function closeSetState() {
  if (busy.value) return
  editingRow.value = null
  stateDraft.value = ''
}
async function setState() {
  const row = editingRow.value
  if (!row || busy.value || stateDraft.value.length !== settings.expected_length) return
  busy.value = true
  actionKey.value = pairKey(row)
  try {
    await upstreamStateApi.setState(row.account_id, row.model, stateDraft.value)
    editingRow.value = null
    stateDraft.value = ''
    app.showSuccess(t(`${key}.stateSet`))
    await loadMatrix()
  } catch (err: unknown) {
    error.value = extractApiErrorMessage(err, t(`${key}.stateSetFailed`))
  } finally {
    busy.value = false
    actionKey.value = ''
  }
}
async function refreshState(row: UpstreamStateMatrixRow) {
  if (busy.value || !row.enabled) return
  busy.value = true
  actionKey.value = pairKey(row)
  try {
    await upstreamStateApi.refresh(row.account_id, row.model)
    app.showSuccess(t(`${key}.refreshSucceeded`))
    await loadMatrix()
  } catch (err: unknown) {
    const message = extractApiErrorMessage(err, t(`${key}.refreshFailed`))
    await loadMatrix(true)
    error.value = message
  } finally {
    busy.value = false
    actionKey.value = ''
  }
}

onMounted(async () => {
  try {
    await loadSettings()
    await loadMatrix()
  } catch (err: unknown) {
    error.value = extractApiErrorMessage(err, t(`${key}.loadFailed`))
  }
  let ticks = 0
  timer = setInterval(() => {
    now.value = Date.now()
    if (++ticks % 15 === 0 && !busy.value && !document.hidden) void loadMatrix()
  }, 1000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>
