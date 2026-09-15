<template>
  <article class="plan-card" :class="{ 'plan-card--owned': isRenewal }">
    <div class="plan-card__name min-w-0">
      <span class="plan-card__icon" aria-hidden="true"><Icon name="bolt" size="sm" /></span>
      <h3 :title="plan.name" class="min-w-0 flex-1 break-words [overflow-wrap:anywhere] text-base font-semibold">{{ plan.name }}</h3>
      <span v-if="discountText" class="plan-card__discount">{{ discountText }}</span>
    </div>

    <div class="plan-card__price min-w-0">
      <span class="plan-card__currency">{{ planCurrencySymbol }}</span>
      <span class="plan-card__value">{{ plan.price }}</span>
      <span v-if="plan.currency" class="plan-card__currency-code">{{ plan.currency }}</span>
      <span class="plan-card__period">/ {{ validitySuffix }}</span>
      <del v-if="plan.original_price && plan.original_price > plan.price">{{ planCurrencySymbol }}{{ plan.original_price }}<template v-if="plan.currency">{{ plan.currency }}</template></del>
    </div>

    <p v-if="plan.description" class="plan-card__description">{{ plan.description }}</p>

    <dl class="plan-card__limits">
      <div v-if="plan.daily_limit_usd != null">
        <dt><Icon name="bolt" size="sm" aria-hidden="true" />{{ t('payment.planCard.dailyLimit') }}</dt>
        <dd>${{ plan.daily_limit_usd }}</dd>
      </div>
      <div v-if="plan.weekly_limit_usd != null">
        <dt><Icon name="calendar" size="sm" aria-hidden="true" />{{ t('payment.planCard.weeklyLimit') }}</dt>
        <dd>${{ plan.weekly_limit_usd }}</dd>
      </div>
      <div v-if="plan.monthly_limit_usd != null">
        <dt><Icon name="calendar" size="sm" aria-hidden="true" />{{ t('payment.planCard.monthlyLimit') }}</dt>
        <dd>${{ plan.monthly_limit_usd }}</dd>
      </div>
      <div v-if="plan.daily_limit_usd == null && plan.weekly_limit_usd == null && plan.monthly_limit_usd == null">
        <dt><Icon name="sparkles" size="sm" aria-hidden="true" />{{ t('payment.planCard.quota') }}</dt>
        <dd>{{ t('payment.planCard.unlimited') }}</dd>
      </div>
    </dl>

    <ul v-if="plan.features.length > 0" class="plan-card__features">
      <li v-for="feature in plan.features" :key="feature">
        <Icon name="check" size="sm" aria-hidden="true" />
        <span>{{ feature }}</span>
      </li>
    </ul>

    <div class="plan-card__action">
      <button type="button" @click="emit('select', plan)">
        <span>{{ isRenewal ? t('payment.renewNow') : t('payment.subscribeNow') }}</span>
        <Icon name="arrowRight" size="sm" aria-hidden="true" />
      </button>
    </div>
  </article>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SubscriptionPlan } from '@/types/payment'
import type { UserSubscription } from '@/types'
import Icon from '@/components/icons/Icon.vue'
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

<style scoped>
.plan-card {
  position: relative;
  isolation: isolate;
  display: flex;
  min-width: 0;
  flex-direction: column;
  overflow: hidden;
  padding: 24px;
  @apply border border-gray-200 bg-white text-gray-900 shadow-sm dark:border-dark-600 dark:bg-dark-800 dark:text-gray-100;
  border-radius: 22px;
  transition: transform 180ms ease, border-color 180ms ease, box-shadow 180ms ease;
}
.plan-card::before {
  content: '';
  position: absolute;
  top: 0;
  right: 24px;
  left: 24px;
  height: 1px;
  @apply bg-primary-200 dark:bg-primary-700;
}
.plan-card:hover {
  transform: translateY(-3px);
  @apply border-primary-400 shadow-md dark:border-primary-500;
}
.plan-card--owned { @apply border-emerald-500 dark:border-emerald-600; }
.plan-card__name { display: flex; align-items: center; gap: 10px; }
.plan-card__name { min-width: 0; }
.plan-card__name h3 { overflow-wrap: anywhere; font-size: 18px; font-weight: 650; line-height: 1.4; }
.plan-card__icon {
  display: grid;
  width: 32px;
  height: 32px;
  flex-shrink: 0;
  place-items: center;
  @apply border border-primary-100 bg-primary-50 text-primary-600 dark:border-primary-800 dark:bg-primary-900/30 dark:text-primary-300;
  border-radius: 10px;
}
.plan-card__discount {
  flex-shrink: 0;
  padding: 4px 9px;
  @apply border border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300;
  border-radius: 7px;
  font-size: 12px;
  font-weight: 650;
}
.plan-card__price { display: flex; flex-wrap: wrap; align-items: baseline; gap: 5px; margin-top: 20px; }
.plan-card__currency { font-size: 19px; @apply text-primary-600 dark:text-primary-300; }
.plan-card__value { font-size: 42px; font-weight: 700; line-height: 1.15; letter-spacing: 0; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.plan-card__period, .plan-card__currency-code { font-size: 13px; @apply text-gray-500 dark:text-gray-400; }
.plan-card__price del { margin-left: auto; font-size: 13px; @apply text-gray-500 dark:text-gray-400; }
.plan-card__description { margin-top: 12px; font-size: 13px; line-height: 1.7; overflow-wrap: anywhere; @apply text-gray-600 dark:text-gray-300; }
.plan-card__limits { display: grid; gap: 1px; margin-top: 18px; overflow: hidden; border-radius: 12px; @apply border border-gray-200 bg-gray-200 dark:border-dark-600 dark:bg-dark-600; }
.plan-card__limits > div { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 8px; padding: 11px 13px; @apply bg-gray-50 dark:bg-dark-900; }
.plan-card__limits dt { display: flex; align-items: center; gap: 8px; font-size: 12px; @apply text-gray-600 dark:text-gray-300; }
.plan-card__limits dt svg { flex-shrink: 0; @apply text-primary-500 dark:text-primary-400; }
.plan-card__limits dd { font-size: 15px; font-weight: 650; overflow-wrap: anywhere; @apply text-gray-900 dark:text-gray-100; }
.plan-card__features { display: grid; gap: 8px; margin-top: 16px; }
.plan-card__features li { display: flex; align-items: flex-start; gap: 8px; font-size: 13px; overflow-wrap: anywhere; @apply text-gray-600 dark:text-gray-300; }
.plan-card__features li span { min-width: 0; }
.plan-card__features svg { flex-shrink: 0; margin-top: 2px; @apply text-emerald-600 dark:text-emerald-400; }
.plan-card__action { margin-top: auto; padding-top: 18px; }
.plan-card__action button {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  width: 100%;
  min-height: 44px;
  padding: 10px 16px;
  @apply border border-primary-600 bg-primary-600 text-white shadow-sm dark:border-primary-500 dark:bg-primary-500;
  border-radius: 11px;
  font-size: 14px;
  font-weight: 600;
  transition: filter 180ms ease;
}
.plan-card__action button:hover { filter: brightness(1.15); }
.plan-card__action button:focus-visible { outline-width: 2px; outline-style: solid; outline-offset: 4px; @apply outline-primary-500 dark:outline-primary-300; }
@media (max-width: 480px) { .plan-card { padding: 20px; } }
@media (prefers-reduced-motion: reduce) { .plan-card, .plan-card__action button { transition: none; } .plan-card:hover { transform: none; } }
</style>
