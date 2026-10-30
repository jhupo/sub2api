<template>
  <section class="space-y-4">
    <form class="grid grid-cols-2 items-end gap-3 sm:flex sm:flex-wrap" @submit.prevent="load">
      <label class="min-w-0 text-sm sm:flex-1">{{ t('admin.accounts.cacheReadAdjustment.startDate') }}
        <input v-model="startDate" type="date" required :max="endDate" class="input mt-1 w-full" />
      </label>
      <label class="min-w-0 text-sm sm:flex-1">{{ t('admin.accounts.cacheReadAdjustment.endDate') }}
        <input v-model="endDate" type="date" required :min="startDate" class="input mt-1 w-full" />
      </label>
      <label class="min-w-0 text-sm">{{ t('admin.accounts.cacheReadAdjustment.accountID') }}
        <input v-model="accountID" type="number" min="1" step="1" class="input mt-1 w-full sm:w-28" :placeholder="t('admin.accounts.cacheReadAdjustment.allAccounts')" />
      </label>
      <button type="submit" class="btn btn-secondary h-10 w-10 shrink-0 justify-self-end p-0" :disabled="loading" :title="t('common.refresh')" :aria-label="t('common.refresh')">
        <Icon name="refresh" :class="{ 'animate-spin': loading }" />
      </button>
    </form>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <div v-if="loading" role="status" class="flex h-32 items-center justify-center"><Icon name="refresh" class="animate-spin" /></div>
    <template v-else-if="!error">
      <dl class="grid grid-cols-1 gap-3 border-y border-gray-200 py-4 text-sm sm:grid-cols-3 dark:border-dark-600">
        <div><dt class="text-gray-500">{{ t('admin.accounts.cacheReadAdjustment.balanceRevenue') }}</dt><dd class="mt-1 break-words text-lg font-semibold tabular-nums">{{ money(totals.balance) }}</dd></div>
        <div><dt class="text-gray-500">{{ t('admin.accounts.cacheReadAdjustment.subscriptionUsage') }}</dt><dd class="mt-1 break-words text-lg font-semibold tabular-nums">{{ money(totals.subscription) }}</dd></div>
        <div><dt class="text-gray-500">{{ t('admin.accounts.cacheReadAdjustment.shiftedTokens') }}</dt><dd class="mt-1 break-words text-lg font-semibold tabular-nums">{{ totals.tokens.toLocaleString() }}</dd></div>
      </dl>
      <div class="max-h-80 overflow-auto">
        <table class="w-full whitespace-nowrap text-left text-sm">
          <thead class="sticky top-0 bg-white text-xs text-gray-500 dark:bg-dark-800"><tr>
            <th v-for="key in ['day', 'accountID', 'pricedRequests', 'shiftedTokens', 'balanceRevenue', 'subscriptionUsage']" :key="key" class="px-2 py-3 font-medium">{{ t(`admin.accounts.cacheReadAdjustment.${key}`) }}</th>
          </tr></thead>
          <tbody><tr v-for="row in rows" :key="`${row.date}-${row.account_id}`" class="border-t border-gray-100 tabular-nums dark:border-dark-700">
            <td class="px-2 py-3">{{ row.date }}</td><td class="px-2 py-3">#{{ row.account_id }}</td>
            <td class="px-2 py-3">{{ row.priced_requests }} / {{ row.requests }}</td><td class="px-2 py-3">{{ row.shifted_tokens.toLocaleString() }}</td>
            <td class="px-2 py-3">{{ money(row.balance_additional_cost) }}</td><td class="px-2 py-3">{{ money(row.subscription_additional_cost) }}</td>
          </tr><tr v-if="!rows.length"><td colspan="6" class="py-8 text-center text-gray-500">{{ t('common.noData') }}</td></tr></tbody>
        </table>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getCacheReadAdjustmentDaily, type CacheReadAdjustmentDay } from '@/api/admin/usage'
import { extractApiErrorMessage } from '@/utils/apiError'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()
const dateString = (date: Date) => [date.getFullYear(), String(date.getMonth() + 1).padStart(2, '0'), String(date.getDate()).padStart(2, '0')].join('-')
const today = new Date()
const firstDay = new Date(today)
firstDay.setDate(firstDay.getDate() - 29)
const startDate = ref(dateString(firstDay))
const endDate = ref(dateString(today))
const accountID = ref('')
const rows = ref<CacheReadAdjustmentDay[]>([])
const loading = ref(false)
const error = ref('')
const totals = computed(() => rows.value.reduce((sum, row) => ({
  balance: sum.balance + row.balance_additional_cost,
  subscription: sum.subscription + row.subscription_additional_cost,
  tokens: sum.tokens + row.shifted_tokens
}), { balance: 0, subscription: 0, tokens: 0 }))
const money = (amount: number) => new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 6, maximumFractionDigits: 6 }).format(amount)
async function load() {
  loading.value = true
  error.value = ''
  try {
    rows.value = await getCacheReadAdjustmentDaily({ start_date: startDate.value, end_date: endDate.value,
      timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
      ...(accountID.value ? { account_id: Number(accountID.value) } : {}) })
  } catch (e) {
    error.value = extractApiErrorMessage(e, t('admin.accounts.cacheReadAdjustment.statsFailed'))
  } finally {
    loading.value = false
  }
}
onMounted(load)
</script>
