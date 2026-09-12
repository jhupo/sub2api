<template>
  <article class="plan-card" :class="{ 'plan-card--owned': isRenewal }">
    <span v-if="discountText" class="plan-card__discount">{{ discountText }}</span>
    <div class="plan-card__name min-w-0">
      <span class="plan-card__icon" aria-hidden="true"><Icon name="bolt" size="sm" /></span>
      <h3 :title="plan.name" class="min-w-0 break-words [overflow-wrap:anywhere] text-base font-semibold">{{ plan.name }}</h3>
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
  color: #f1f5ff;
  border: 1px solid #334b70;
  border-radius: 22px;
  background: radial-gradient(ellipse at 100% 0%, #1a3e60 0%, transparent 60%), #101d33;
  box-shadow: 0 16px 36px -16px rgb(15 35 70 / 45%);
  transition: transform 180ms ease, border-color 180ms ease, box-shadow 180ms ease;
}
.plan-card::before {
  content: '';
  position: absolute;
  top: 0;
  right: 24px;
  left: 24px;
  height: 1px;
  background: linear-gradient(90deg, transparent, #7aa5ff, #6be4ea, transparent);
}
.plan-card:hover {
  transform: translateY(-3px);
  border-color: #638fc4;
  box-shadow: 0 24px 45px -20px rgb(27 78 157 / 55%);
}
.plan-card--owned { border-color: #398c84; }
.plan-card__name { display: flex; align-items: center; gap: 10px; }
.plan-card__name { min-width: 0; }
.plan-card__name h3 { overflow-wrap: anywhere; font-size: 18px; font-weight: 650; line-height: 1.4; }
.plan-card__icon {
  display: grid;
  width: 32px;
  height: 32px;
  flex-shrink: 0;
  place-items: center;
  color: #9ce8f4;
  border: 1px solid rgb(148 213 245 / 20%);
  border-radius: 10px;
  background: rgb(113 184 247 / 10%);
}
.plan-card__discount {
  position: absolute;
  top: 24px;
  right: 24px;
  flex-shrink: 0;
  padding: 4px 9px;
  color: #9ef0d3;
  border: 1px solid rgb(89 221 178 / 24%);
  border-radius: 7px;
  background: rgb(43 178 143 / 12%);
  font-size: 12px;
  font-weight: 650;
}
.plan-card__price { display: flex; flex-wrap: wrap; align-items: baseline; gap: 5px; margin-top: 20px; }
.plan-card__currency { font-size: 19px; color: #acd2ff; }
.plan-card__value { font-size: 42px; font-weight: 700; line-height: 1.15; letter-spacing: -1.5px; font-variant-numeric: tabular-nums; overflow-wrap: anywhere; }
.plan-card__period, .plan-card__currency-code { color: #b4c5dd; font-size: 13px; }
.plan-card__price del { margin-left: auto; color: #a5b6cf; font-size: 13px; }
.plan-card__description { margin-top: 12px; color: #bdcbe0; font-size: 13px; line-height: 1.7; overflow-wrap: anywhere; }
.plan-card__limits { display: grid; gap: 1px; margin-top: 18px; overflow: hidden; border: 1px solid rgb(155 186 228 / 15%); border-radius: 12px; background: rgb(155 186 228 / 10%); }
.plan-card__limits > div { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 8px; padding: 11px 13px; background: #16283f; }
.plan-card__limits dt { display: flex; align-items: center; gap: 8px; color: #b8c9df; font-size: 12px; }
.plan-card__limits dt svg { color: #90b3e4; flex-shrink: 0; }
.plan-card__limits dd { color: #e0f7ff; font-size: 15px; font-weight: 650; overflow-wrap: anywhere; }
.plan-card__features { display: grid; gap: 8px; margin-top: 16px; }
.plan-card__features li { display: flex; align-items: flex-start; gap: 8px; color: #bdcbe0; font-size: 13px; overflow-wrap: anywhere; }
.plan-card__features svg { flex-shrink: 0; margin-top: 2px; color: #82e3c4; }
.plan-card__action { margin-top: auto; padding-top: 18px; }
.plan-card__action button {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  width: 100%;
  min-height: 44px;
  padding: 10px 16px;
  color: #fff;
  border: 1px solid rgb(139 174 255 / 50%);
  border-radius: 11px;
  background: linear-gradient(105deg, #2860dc, #5550d8);
  box-shadow: 0 6px 18px rgb(18 37 106 / 30%);
  font-size: 14px;
  font-weight: 600;
  transition: filter 180ms ease;
}
.plan-card__action button:hover { filter: brightness(1.15); }
.plan-card__action button:focus-visible { outline: 2px solid #a1e9ff; outline-offset: 4px; }
@media (max-width: 480px) { .plan-card { padding: 20px; } }
@media (prefers-reduced-motion: reduce) { .plan-card, .plan-card__action button { transition: none; } .plan-card:hover { transform: none; } }
</style>
