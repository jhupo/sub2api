<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <p class="text-sm text-gray-500 dark:text-gray-400" data-testid="displayed-range" aria-live="polite">
          {{ displayedRange }}
        </p>
        <div class="flex flex-wrap items-center gap-2">
          <div class="inline-flex shrink-0 overflow-hidden rounded-lg border border-gray-200 dark:border-dark-600">
            <button
              v-for="option in periodOptions"
              :key="option.value"
              type="button"
              class="whitespace-nowrap px-3 py-2 text-sm font-medium transition-colors"
              :aria-pressed="period === option.value"
              :data-testid="`period-${option.value}`"
              :class="period === option.value
                ? 'bg-primary-600 text-white'
                : 'text-gray-600 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700'"
              @click="selectPeriod(option.value)"
            >
              {{ option.label }}
            </button>
          </div>
          <button @click="loadDashboard" :disabled="loading" class="btn btn-secondary" :title="t('common.refresh')" :aria-label="t('common.refresh')">
            <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
          </button>
        </div>
      </div>

      <!-- Dashboard Content -->
      <div v-if="loading" class="flex items-center justify-center py-12">
        <LoadingSpinner />
      </div>
      <template v-else-if="stats">
        <OrderStatsCards :stats="stats" />
        <DailyRevenueChart :data="stats.daily_series || []" :loading="loading" />
        <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
          <div class="card p-4">
            <h3 class="mb-4 text-sm font-semibold text-gray-900 dark:text-white">{{ t('payment.admin.paymentDistribution') }}</h3>
            <div v-if="!stats.payment_methods?.length" class="flex h-32 items-center justify-center text-sm text-gray-500 dark:text-gray-400">{{ t('payment.admin.noData') }}</div>
            <div v-else class="space-y-3">
              <div v-for="method in stats.payment_methods" :key="method.type" class="flex items-center justify-between">
                <div class="flex items-center gap-2">
                  <span :class="['inline-block h-3 w-3 rounded-full', methodColor(method.type)]"></span>
                  <span class="text-sm text-gray-700 dark:text-gray-300">{{ t('payment.methods.' + method.type, method.type) }}</span>
                </div>
                <div class="space-y-1 text-right">
                  <span v-for="[currency, amount] in sortedAmounts(method.amount)" :key="currency" class="block text-sm font-medium text-gray-900 dark:text-white">{{ formatMoney(currency, amount) }}</span>
                  <span class="ml-2 text-xs text-gray-500 dark:text-gray-400">({{ method.count }})</span>
                </div>
              </div>
            </div>
          </div>
          <div class="card p-4">
            <h3 class="mb-4 text-sm font-semibold text-gray-900 dark:text-white">{{ t('payment.admin.topUsers') }}</h3>
            <div v-if="!hasTopUsers(stats.top_users)" class="flex h-32 items-center justify-center text-sm text-gray-500 dark:text-gray-400">{{ t('payment.admin.noData') }}</div>
            <div v-else class="space-y-2">
              <div v-for="[currency, users] in sortedTopUsers(stats.top_users)" :key="currency" class="space-y-2">
                <p class="text-xs font-semibold text-gray-500 dark:text-gray-400">{{ currency }}</p>
                <div v-for="(user, idx) in users" :key="user.user_id" class="flex items-center justify-between rounded-lg px-3 py-2 hover:bg-gray-50 dark:hover:bg-dark-700">
                  <div class="flex items-center gap-3">
                    <span :class="['flex h-6 w-6 items-center justify-center rounded-full text-xs font-bold', rankClass(idx)]">{{ idx + 1 }}</span>
                    <span class="text-sm text-gray-700 dark:text-gray-300">{{ user.email }}</span>
                  </div>
                  <span class="text-sm font-medium text-gray-900 dark:text-white">{{ formatMoney(currency, user.amount) }}</span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </template>
    </div>
    <BaseDialog :show="showCustomRange" :title="t('payment.admin.customDateRange')" width="narrow" @close="showCustomRange = false">
      <form id="payment-date-range" class="space-y-4" @submit.prevent="applyCustomRange">
        <div>
          <label for="payment-start-date" class="input-label">{{ t('payment.admin.startDate') }}</label>
          <input id="payment-start-date" v-model="draftStartDate" type="date" class="input" required :max="draftEndDate || undefined" />
        </div>
        <div>
          <label for="payment-end-date" class="input-label">{{ t('payment.admin.endDate') }}</label>
          <input id="payment-end-date" v-model="draftEndDate" type="date" class="input" required :min="draftStartDate || undefined" />
        </div>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.admin.dateRangeHint') }}</p>
        <p v-if="rangeError" role="alert" class="text-sm text-red-600">{{ rangeError }}</p>
      </form>
      <template #footer>
        <button type="button" class="btn btn-secondary" @click="showCustomRange = false">{{ t('common.cancel') }}</button>
        <button type="submit" form="payment-date-range" class="btn btn-primary" :disabled="!!rangeError" data-testid="apply-range">{{ t('payment.admin.applyDateRange') }}</button>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminPaymentAPI, type PaymentDashboardParams } from '@/api/admin/payment'
import { extractI18nErrorMessage } from '@/utils/apiError'
import type { CurrencyAmounts, DashboardStats, TopUserPaymentStats } from '@/types/payment'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Icon from '@/components/icons/Icon.vue'
import OrderStatsCards from '@/components/admin/payment/OrderStatsCards.vue'
import DailyRevenueChart from '@/components/admin/payment/DailyRevenueChart.vue'

const { t } = useI18n()
const appStore = useAppStore()

type Period = 7 | 30 | 90 | 'custom'
const periodOptions = computed(() => [
  ...([7, 30, 90] as const).map(value => ({ value, label: `${value}${t('payment.admin.daySuffix')}` })),
  { value: 'custom' as const, label: t('payment.admin.customPeriod') }
])
const period = ref<Period>(30)
const customRange = ref<{ start_date: string; end_date: string } | null>(null)
const showCustomRange = ref(false)
const draftStartDate = ref('')
const draftEndDate = ref('')
const loading = ref(false)
const stats = ref<DashboardStats | null>(null)
let requestSequence = 0

const displayedRange = computed(() => {
  const series = stats.value?.daily_series
  if (!series?.length || loading.value) return ''
  return t('payment.admin.selectedDateRange', { start: series[0].date, end: series[series.length - 1].date })
})
const rangeError = computed(() => {
  if (!draftStartDate.value || !draftEndDate.value) return t('payment.admin.dateRangeRequired')
  if (draftStartDate.value > draftEndDate.value) return t('payment.admin.dateRangeInvalid')
  return ''
})

function methodColor(type: string): string {
  const c: Record<string, string> = {
    alipay: 'bg-blue-500', wxpay: 'bg-green-500',
    alipay_direct: 'bg-blue-400', wxpay_direct: 'bg-green-400',
    stripe: 'bg-purple-500',
  }
  return c[type] || 'bg-gray-400'
}

function rankClass(idx: number): string {
  if (idx === 0) return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'
  if (idx === 1) return 'bg-gray-200 text-gray-600 dark:bg-gray-700 dark:text-gray-300'
  if (idx === 2) return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400'
  return 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
}

function sortedAmounts(amounts: CurrencyAmounts): [string, number][] {
  return Object.entries(amounts).sort(([left], [right]) => left.localeCompare(right))
}

function sortedTopUsers(usersByCurrency: Record<string, TopUserPaymentStats[]>): [string, TopUserPaymentStats[]][] {
  return Object.entries(usersByCurrency).sort(([left], [right]) => left.localeCompare(right))
}

function hasTopUsers(usersByCurrency: Record<string, TopUserPaymentStats[]>): boolean {
  return Object.values(usersByCurrency).some(users => users.length > 0)
}

function formatMoney(currency: string, amount: number): string {
  return new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(amount)
}

async function loadDashboard() {
  const query: PaymentDashboardParams = period.value === 'custom'
    ? { ...customRange.value! }
    : { days: period.value }
  const sequence = ++requestSequence
  loading.value = true
  try {
    const res = await adminPaymentAPI.getDashboard(query)
    if (sequence === requestSequence) stats.value = res.data
  } catch (err: unknown) {
    if (sequence === requestSequence) {
      stats.value = null
      appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
    }
  } finally {
    if (sequence === requestSequence) loading.value = false
  }
}

function selectPeriod(value: Period) {
  if (value === 'custom') {
    const series = stats.value?.daily_series
    draftStartDate.value = customRange.value?.start_date || series?.[0]?.date || ''
    draftEndDate.value = customRange.value?.end_date || series?.[series.length - 1]?.date || ''
    showCustomRange.value = true
    return
  }
  period.value = value
  void loadDashboard()
}

function applyCustomRange() {
  if (rangeError.value) return
  customRange.value = { start_date: draftStartDate.value, end_date: draftEndDate.value }
  period.value = 'custom'
  showCustomRange.value = false
  void loadDashboard()
}

onMounted(() => loadDashboard())
onUnmounted(() => { requestSequence++ })
</script>
