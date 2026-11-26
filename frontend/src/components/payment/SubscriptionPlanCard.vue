<template>
  <article
    ref="cardElement"
    class="plan-card"
    :class="[
      `plan-card--${appearance.theme}`,
      `plan-card--${appearance.motion}`,
      { 'plan-card--owned': isRenewal, 'plan-card--paused': !animate }
    ]"
  >
    <div class="plan-card__art" aria-hidden="true"><span></span></div>
    <div class="plan-card__name min-w-0">
      <span class="plan-card__icon" aria-hidden="true"><Icon name="bolt" size="sm" /></span>
      <h3 :title="plan.name" class="min-w-0 flex-1 break-words [overflow-wrap:anywhere] text-base font-semibold">{{ plan.name }}</h3>
      <span v-if="discountText" class="plan-card__discount">{{ discountText }}</span>
    </div>

    <div class="plan-card__price min-w-0">
      <span class="plan-card__currency">{{ planCurrencySymbol }}</span>
      <span class="plan-card__value">{{ plan.price }}</span>
      <span v-if="plan.currency" class="plan-card__currency-code">{{ plan.currency }}</span>
      <span v-if="plan.card_mode !== 'deadline'" class="plan-card__period">/ {{ validitySuffix }}</span>
      <del v-if="plan.original_price && plan.original_price > plan.price">{{ planCurrencySymbol }}{{ plan.original_price }}<template v-if="plan.currency">{{ plan.currency }}</template></del>
    </div>

    <p v-if="plan.description" class="plan-card__description">{{ plan.description }}</p>
    <SaleCountdown v-if="plan.sale_ends_at" :ends-at="plan.sale_ends_at" class="mt-4" />

    <dl class="plan-card__limits">
      <div v-if="plan.card_mode === 'deadline' && plan.deadline_at">
        <dt><Icon name="calendar" size="sm" aria-hidden="true" />{{ t('quotaFeatures.deadline') }}</dt>
        <dd><time :datetime="plan.deadline_at">{{ formatDateTimeToMinute(plan.deadline_at) }}</time></dd>
      </div>
      <div v-if="plan.total_limit_usd != null">
        <dt>{{ t('quotaFeatures.totalLimit') }}</dt>
        <dd>${{ plan.total_limit_usd }}</dd>
      </div>
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
      <div v-if="plan.total_limit_usd == null && plan.daily_limit_usd == null && plan.weekly_limit_usd == null && plan.monthly_limit_usd == null">
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
      <button type="button" :disabled="preview" @click="emit('select', plan)">
        <span>{{ isRenewal ? t('payment.renewNow') : t('payment.subscribeNow') }}</span>
        <Icon name="arrowRight" size="sm" aria-hidden="true" />
      </button>
    </div>
  </article>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useDocumentVisibility, useIntersectionObserver, usePreferredReducedMotion } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import type { SubscriptionPlan } from '@/types/payment'
import type { UserSubscription } from '@/types'
import Icon from '@/components/icons/Icon.vue'
import { isStandaloneQuotaPlan, planValiditySuffix } from './validity'
import { currencySymbol } from '@/components/payment/currency'
import { formatDateTimeToMinute } from '@/utils/format'
import SaleCountdown from './SaleCountdown.vue'
import { normalizePlanCardAppearance } from './planAppearance'

const props = defineProps<{ plan: SubscriptionPlan; activeSubscriptions?: UserSubscription[]; preview?: boolean }>()
const emit = defineEmits<{ select: [plan: SubscriptionPlan] }>()
const { t } = useI18n()
const appearance = computed(() => normalizePlanCardAppearance(props.plan.card_appearance))
const cardElement = ref<HTMLElement | null>(null)
const visible = ref(true)
const documentVisibility = useDocumentVisibility()
const reducedMotion = usePreferredReducedMotion()
useIntersectionObserver(cardElement, ([entry]) => { visible.value = entry?.isIntersecting ?? false })
const animate = computed(() => visible.value && documentVisibility.value === 'visible' && reducedMotion.value !== 'reduce')

const isRenewal = computed(() =>
  !isStandaloneQuotaPlan(props.plan) && (props.activeSubscriptions?.some(s => s.plan_id === props.plan.id && s.status === 'active') ?? false)
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
  --plan-accent: #2563eb;
  --plan-tint: #eff6ff;
  --plan-line: #dbeafe;
  --plan-art: #cbd5e1;
  position: relative;
  isolation: isolate;
  display: flex;
  min-width: 0;
  flex-direction: column;
  overflow: hidden;
  padding: 28px 24px 24px;
  @apply border border-gray-200 bg-white text-gray-900 shadow-sm dark:border-dark-600 dark:bg-dark-800 dark:text-gray-100;
  border-radius: 8px;
  transition: transform 180ms ease, border-color 180ms ease, box-shadow 180ms ease;
}
.plan-card--mint { --plan-accent: #047857; --plan-tint: #ecfdf5; --plan-line: #a7f3d0; --plan-art: #6ee7b7; }
.plan-card--ruby { --plan-accent: #be123c; --plan-tint: #fff1f2; --plan-line: #fecdd3; --plan-art: #fda4af; }
.dark .plan-card { --plan-accent: #93c5fd; --plan-tint: #172554; --plan-line: #1e40af; --plan-art: #475569; }
.dark .plan-card--mint { --plan-accent: #6ee7b7; --plan-tint: #063d31; --plan-line: #065f46; --plan-art: #10b981; }
.dark .plan-card--ruby { --plan-accent: #fda4af; --plan-tint: #4c1026; --plan-line: #881337; --plan-art: #f43f5e; }
.plan-card__art {
  position: absolute;
  z-index: -1;
  top: 0;
  right: 0;
  left: 0;
  height: 110px;
  overflow: hidden;
  pointer-events: none;
  background: var(--plan-tint);
  opacity: .48;
  mask-image: linear-gradient(#000, transparent);
}
.plan-card__art::before, .plan-card__art::after, .plan-card__art span {
  position: absolute;
  content: '';
  pointer-events: none;
}
.plan-card__art::before {
  inset: 0;
  opacity: .4;
  background-image: repeating-linear-gradient(120deg, transparent 0 31px, var(--plan-art) 31px 32px, transparent 32px 48px);
}
.plan-card__art::after {
  top: 0;
  left: 0;
  right: 0;
  height: 3px;
  background: var(--plan-accent);
}
.plan-card__art span {
  inset: 0 auto 0 -60%;
  width: 45%;
  transform: skewX(-22deg);
  background: linear-gradient(90deg, transparent, var(--plan-line), transparent);
  opacity: 0;
}
.plan-card--shine .plan-card__art,
.plan-card--flow .plan-card__art {
  height: 200px;
  opacity: .95;
  mask-image: linear-gradient(#000 0 48%, transparent);
}
.plan-card--shine .plan-card__art span {
  width: 65%;
  background: linear-gradient(90deg, transparent, var(--plan-art) 36%, #fff 50%, var(--plan-art) 64%, transparent);
  animation: plan-shine 3.2s ease-in-out infinite;
}
.plan-card--flow .plan-card__art::before {
  inset: 0 -96px;
  opacity: .7;
  background-image: repeating-linear-gradient(135deg, transparent 0 21%, var(--plan-art) 21% 25%, transparent 25% 50%);
  background-size: 96px 96px;
  animation: plan-flow 3.2s linear infinite;
}
.plan-card--paused .plan-card__art span, .plan-card--paused .plan-card__art::before { animation-play-state: paused; }
@keyframes plan-shine {
  0% { transform: translateX(0) skewX(-22deg); opacity: 0; }
  12%, 56% { opacity: .65; }
  76%, 100% { transform: translateX(340%) skewX(-22deg); opacity: 0; }
}
@keyframes plan-flow {
  from { transform: translateX(0); }
  to { transform: translateX(96px); }
}
.plan-card:hover {
  transform: translateY(-2px);
  border-color: var(--plan-accent);
  @apply shadow-md;
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
  color: var(--plan-accent);
  background: var(--plan-tint);
  border: 1px solid var(--plan-line);
  border-radius: 8px;
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
.plan-card__currency { font-size: 18px; color: var(--plan-accent); }
.plan-card__value { font-size: 36px; font-weight: 700; line-height: 1.15; letter-spacing: 0; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.plan-card__period, .plan-card__currency-code { font-size: 13px; @apply text-gray-500 dark:text-gray-400; }
.plan-card__price del { margin-left: auto; font-size: 13px; @apply text-gray-500 dark:text-gray-400; }
.plan-card__description { margin-top: 12px; font-size: 13px; line-height: 1.7; overflow-wrap: anywhere; @apply text-gray-600 dark:text-gray-300; }
.plan-card__limits { display: grid; margin-top: 18px; @apply border-y border-gray-100 dark:border-dark-600; }
.plan-card__limits > div { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 8px; padding: 11px 0; }
.plan-card__limits > div + div { @apply border-t border-gray-100 dark:border-dark-700; }
.plan-card__limits dt { display: flex; align-items: center; gap: 8px; font-size: 12px; @apply text-gray-600 dark:text-gray-300; }
.plan-card__limits dt svg { flex-shrink: 0; color: var(--plan-accent); }
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
  background: var(--plan-accent);
  border: 1px solid var(--plan-accent);
  color: #fff;
  border-radius: 6px;
  font-size: 14px;
  font-weight: 600;
  transition: filter 180ms ease;
}
.plan-card__action button:hover { filter: brightness(1.15); }
.plan-card__action button:disabled { cursor: default; }
.dark .plan-card__action button { color: #111827; }
.plan-card__action button:focus-visible { outline-width: 2px; outline-style: solid; outline-offset: 4px; @apply outline-primary-500 dark:outline-primary-300; }
@media (max-width: 480px) { .plan-card { padding: 20px; } }
@media (prefers-reduced-motion: reduce) {
  .plan-card, .plan-card__action button { transition: none; }
  .plan-card:hover { transform: none; }
  .plan-card--shine .plan-card__art span,
  .plan-card--flow .plan-card__art::before { animation: none; }
}
</style>
