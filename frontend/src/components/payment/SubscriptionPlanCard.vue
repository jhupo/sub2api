<template>
  <div
    :class="[
      'group relative flex flex-col overflow-hidden rounded-lg border border-gray-200 transition-all dark:border-dark-600',
      'hover:border-primary-300',
      'bg-white dark:bg-dark-800',
    ]"
  >
    <!-- Colored top accent bar -->
    <div class="h-1 bg-primary-500" />

    <div class="flex flex-1 flex-col p-4">
      <div class="mb-3 min-w-0 space-y-2">
        <div class="min-w-0">
          <h3
            :title="plan.name"
            class="min-h-6 min-w-0 break-words [overflow-wrap:anywhere] text-base font-semibold leading-6 text-gray-900 dark:text-white"
          >
            {{ plan.name }}
          </h3>
        </div>
        <div class="min-w-0">
          <div class="flex flex-wrap items-baseline gap-x-1 gap-y-0.5 text-primary-600 dark:text-primary-400">
            <span class="text-sm font-medium">{{ planCurrencySymbol }}</span>
            <span class="break-all text-3xl font-semibold tabular-nums leading-9">{{ plan.price }}</span>
            <span v-if="plan.currency" class="text-xs font-medium">{{ plan.currency }}</span>
            <span class="ml-1 text-xs text-gray-500 dark:text-dark-400">/ {{ validitySuffix }}</span>
          </div>
          <div v-if="plan.original_price && plan.original_price > plan.price" class="mt-0.5 flex flex-wrap items-center gap-1.5">
            <span class="text-xs text-gray-400 line-through dark:text-dark-500">{{ planCurrencySymbol }}{{ plan.original_price }}<template v-if="plan.currency"> {{ plan.currency }}</template></span>
            <span class="rounded bg-rose-50 px-1 py-0.5 text-[10px] font-semibold text-rose-600 dark:bg-rose-900/20 dark:text-rose-300">{{ discountText }}</span>
          </div>
        </div>
        <p v-if="plan.description" class="break-words text-xs leading-5 text-gray-500 dark:text-dark-400">
          {{ plan.description }}
        </p>
      </div>

      <!-- Entitlement limits -->
      <div class="mb-3 space-y-2 border-y border-gray-100 py-3 text-xs dark:border-dark-700">
        <div v-if="plan.daily_limit_usd != null" class="flex flex-wrap items-center justify-between gap-2">
          <span class="text-gray-400 dark:text-dark-500">{{ t('payment.planCard.dailyLimit') }}</span>
          <span class="font-medium text-gray-700 dark:text-gray-300">${{ plan.daily_limit_usd }}</span>
        </div>
        <div v-if="plan.weekly_limit_usd != null" class="flex flex-wrap items-center justify-between gap-2">
          <span class="text-gray-400 dark:text-dark-500">{{ t('payment.planCard.weeklyLimit') }}</span>
          <span class="font-medium text-gray-700 dark:text-gray-300">${{ plan.weekly_limit_usd }}</span>
        </div>
        <div v-if="plan.monthly_limit_usd != null" class="flex flex-wrap items-center justify-between gap-2">
          <span class="text-gray-400 dark:text-dark-500">{{ t('payment.planCard.monthlyLimit') }}</span>
          <span class="font-medium text-gray-700 dark:text-gray-300">${{ plan.monthly_limit_usd }}</span>
        </div>
        <div v-if="plan.daily_limit_usd == null && plan.weekly_limit_usd == null && plan.monthly_limit_usd == null" class="flex items-center justify-between">
          <span class="text-gray-400 dark:text-dark-500">{{ t('payment.planCard.quota') }}</span>
          <span class="font-medium text-gray-700 dark:text-gray-300">{{ t('payment.planCard.unlimited') }}</span>
        </div>
      </div>

      <!-- Features list (compact) -->
      <div v-if="plan.features.length > 0" class="mb-3 space-y-1">
        <div v-for="feature in plan.features" :key="feature" class="flex items-start gap-1.5">
          <svg class="mt-0.5 h-3.5 w-3.5 flex-shrink-0 text-emerald-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2.5">
            <path stroke-linecap="round" stroke-linejoin="round" d="M4.5 12.75l6 6 9-13.5" />
          </svg>
          <span class="text-xs text-gray-600 dark:text-gray-300">{{ feature }}</span>
        </div>
      </div>

      <div class="flex-1" />

      <!-- Subscribe Button -->
      <button
        type="button"
        class="w-full rounded bg-primary-600 py-2.5 text-sm font-semibold text-white transition-colors hover:bg-primary-700"
        @click="emit('select', plan)"
      >
        {{ isRenewal ? t('payment.renewNow') : t('payment.subscribeNow') }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SubscriptionPlan } from '@/types/payment'
import type { UserSubscription } from '@/types'
import { planValiditySuffix } from './validity'
import { currencySymbol } from '@/components/payment/currency'

const props = defineProps<{ plan: SubscriptionPlan; activeSubscriptions?: UserSubscription[] }>()
const emit = defineEmits<{ select: [plan: SubscriptionPlan] }>()
const { t } = useI18n()

const isRenewal = computed(() =>
  props.activeSubscriptions?.some(s => s.plan_id === props.plan.id && s.status === 'active') ?? false
)

const discountText = computed(() => {
  if (!props.plan.original_price || props.plan.original_price <= 0) return ''
  const pct = Math.round((1 - props.plan.price / props.plan.original_price) * 100)
  return pct > 0 ? `-${pct}%` : ''
})

const planCurrencySymbol = computed(() => currencySymbol(props.plan.currency || 'USD'))

const validitySuffix = computed(() => planValiditySuffix(props.plan, t))
</script>
