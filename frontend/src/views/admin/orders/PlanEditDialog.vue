<template>
  <BaseDialog :show="show" :title="plan ? t('payment.admin.editPlan') : t('payment.admin.createPlan')" width="wide" @close="emit('close')">
    <form id="plan-form" @submit.prevent="handleSavePlan" class="space-y-4">
      <div>
        <div>
          <label class="input-label">{{ t('payment.admin.planName') }} <span class="text-red-500">*</span></label>
          <input v-model="planForm.name" type="text" class="input" required />
        </div>
      </div>

      <div><label class="input-label">{{ t('payment.admin.planDescription') }} <span class="text-red-500">*</span></label><textarea v-model="planForm.description" rows="2" class="input" required></textarea></div>
      <div class="grid grid-cols-2 gap-4">
        <div>
          <label class="input-label">{{ t('payment.admin.price') }} <span class="text-red-500">*</span></label>
          <input v-model.number="planForm.price" type="number" step="0.01" min="0.01" class="input" required />
          <p v-if="subscriptionCnyPreview" class="mt-1 text-xs font-medium text-primary-600 dark:text-primary-400">
            {{ t('payment.admin.subscriptionCnyPayPreview', { amount: subscriptionCnyPreview.amount }) }}
            <span v-if="subscriptionCnyPreview.feeRate > 0">
              {{ t('payment.admin.subscriptionCnyPayPreviewWithFee', { feeRate: subscriptionCnyPreview.feeRate, total: subscriptionCnyPreview.total }) }}
            </span>
          </p>
        </div>
        <div><label class="input-label">{{ t('payment.admin.originalPrice') }}</label><input v-model.number="planForm.original_price" type="number" step="0.01" min="0" class="input" /></div>
      </div>
      <label class="block"><span class="input-label">{{ t('quotaFeatures.cardMode') }}</span>
        <select v-model="planForm.card_mode" class="input">
          <option value="">{{ t('quotaFeatures.recurringPlan') }}</option>
          <option value="hours">{{ t('quotaFeatures.hoursCard') }}</option>
          <option value="deadline">{{ t('quotaFeatures.deadlineCard') }}</option>
        </select>
      </label>
      <div v-if="planForm.card_mode" class="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <label class="block"><span class="input-label">{{ t('quotaFeatures.totalLimit') }} (USD)</span><input v-model.number="planForm.total_limit_usd" type="number" min="0.01" step="0.01" class="input" required /></label>
        <label v-if="planForm.card_mode === 'hours'" class="block"><span class="input-label">{{ t('quotaFeatures.validityHours') }}</span><input v-model.number="planForm.validity_hours" type="number" min="1" max="87600" step="1" class="input" required /></label>
        <label v-else class="block"><span class="input-label">{{ t('quotaFeatures.deadline') }}</span><input v-model="planForm.deadline_at" type="datetime-local" class="input" required /></label>
      </div>
      <div v-if="!planForm.card_mode" class="grid grid-cols-2 gap-4">
        <div><label class="input-label">{{ t('payment.admin.validity') }} <span class="text-red-500">*</span></label><input v-model.number="planForm.validity_days" type="number" min="1" class="input" required /></div>
        <div><label class="input-label">{{ t('payment.admin.validityUnit') }} <span class="text-red-500">*</span></label><Select v-model="planForm.validity_unit" :options="validityUnitOptions" /></div>
      </div>
      <PlanSaleEditor v-if="planForm.card_mode" v-model="planForm.sale_schedule" :mode="planForm.card_mode" :timezone="plan?.sale_timezone || 'Asia/Shanghai'" />
      <div v-if="!planForm.card_mode" class="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <div>
          <label class="input-label">{{ t('payment.admin.dailyLimit') }}</label>
          <input v-model.number="planForm.daily_limit_usd" type="number" step="0.01" min="0" class="input" :placeholder="t('payment.admin.unlimited')" />
        </div>
        <div>
          <label class="input-label">{{ t('payment.admin.weeklyLimit') }}</label>
          <input v-model.number="planForm.weekly_limit_usd" type="number" step="0.01" min="0" class="input" :placeholder="t('payment.admin.unlimited')" />
        </div>
        <div>
          <label class="input-label">{{ t('payment.admin.monthlyLimit') }}</label>
          <input v-model.number="planForm.monthly_limit_usd" type="number" step="0.01" min="0" class="input" :placeholder="t('payment.admin.unlimited')" />
        </div>
      </div>
      <div class="grid grid-cols-2 gap-4">
        <div><label class="input-label">{{ t('payment.admin.sortOrder') }}</label><input v-model.number="planForm.sort_order" type="number" min="0" class="input" /></div>
        <div>
          <label class="input-label">{{ t('payment.admin.currency') }}</label>
          <input v-model="planForm.currency" type="text" maxlength="3" class="input uppercase" :placeholder="t('payment.admin.currencyPlaceholder')" />
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('payment.admin.currencyHint') }}</p>
        </div>
      </div>
      <div>
        <label class="input-label">{{ t('payment.admin.features') }}</label>
        <textarea v-model="planFeaturesText" rows="3" class="input" :placeholder="t('payment.admin.featuresPlaceholder')"></textarea>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('payment.admin.featuresHint') }}</p>
      </div>
      <div class="flex items-center gap-3">
        <label class="text-sm text-gray-700 dark:text-gray-300">{{ t('payment.admin.forSale') }}</label>
        <button
          type="button"
          :class="[
            'relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2',
            planForm.for_sale ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-600'
          ]"
          @click="planForm.for_sale = !planForm.for_sale"
        >
          <span :class="[
            'pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out',
            planForm.for_sale ? 'translate-x-5' : 'translate-x-0'
          ]" />
        </button>
      </div>
    </form>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" @click="emit('close')" class="btn btn-secondary">{{ t('common.cancel') }}</button>
        <button type="submit" form="plan-form" :disabled="saving" class="btn btn-primary">{{ saving ? t('common.saving') : t('common.save') }}</button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminPaymentAPI } from '@/api/admin/payment'
import type { AdminPaymentConfig } from '@/api/admin/payment'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatPaymentAmount } from '@/components/payment/currency'
import type { SubscriptionPlan, PlanSaleSchedule } from '@/types/payment'
import PlanSaleEditor from '@/components/payment/PlanSaleEditor.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'

const props = defineProps<{
  show: boolean
  plan: SubscriptionPlan | null
  paymentConfig?: AdminPaymentConfig | null
}>()

const emit = defineEmits<{
  close: []
  saved: []
}>()

const { t } = useI18n()
const appStore = useAppStore()

const saving = ref(false)
const planForm = reactive({
  sale_schedule: { ends_at: null, price_steps: [], daily_windows: [] } as PlanSaleSchedule,
  card_mode: '' as '' | 'hours' | 'deadline',
  validity_hours: 24,
  deadline_at: '',
  total_limit_usd: null as number | null,
  name: '',
  description: '',
  price: 0,
  original_price: 0,
  currency: '',
  validity_days: 30,
  validity_unit: 'days',
  daily_limit_usd: null as number | null,
  weekly_limit_usd: null as number | null,
  monthly_limit_usd: null as number | null,
  sort_order: 0,
  for_sale: true,
})
const planFeaturesText = ref('')

const validityUnitOptions = computed(() => [
  { value: 'days', label: t('payment.admin.days') },
  { value: 'weeks', label: t('payment.admin.weeks') },
  { value: 'months', label: t('payment.admin.months') },
])

function roundCnyAmount(value: number): number {
  return Math.round(value * 100) / 100
}

function ceilCnyAmount(value: number): number {
  return Math.ceil(value * 100) / 100
}

const subscriptionCnyPreview = computed(() => {
  const price = Number(planForm.price) || 0
  const rate = Number(props.paymentConfig?.subscription_usd_to_cny_rate) || 0
  if (price <= 0 || rate <= 0) return null

  const amount = roundCnyAmount(price * rate)
  const feeRate = Number(props.paymentConfig?.recharge_fee_rate) || 0
  const fee = feeRate > 0 ? ceilCnyAmount((amount * feeRate) / 100) : 0
  const total = feeRate > 0 ? roundCnyAmount(amount + fee) : amount

  return {
    amount: formatPaymentAmount(amount, 'CNY'),
    feeRate,
    total: formatPaymentAmount(total, 'CNY'),
  }
})

// Reset form when dialog opens
watch(() => props.show, (visible) => {
  if (!visible) return
  if (props.plan) {
    Object.assign(planForm, {
      sale_schedule: { ends_at: props.plan.sale_schedule?.ends_at ?? null, price_steps: props.plan.sale_schedule?.price_steps?.map(step => ({ ...step })) ?? [], daily_windows: props.plan.sale_schedule?.daily_windows?.map(window => ({ ...window })) ?? [] },
      card_mode: props.plan.card_mode || '',
      validity_hours: props.plan.validity_hours || 24,
      deadline_at: toLocalDateTime(props.plan.deadline_at),
      total_limit_usd: props.plan.total_limit_usd ?? null,
      name: props.plan.name,
      description: props.plan.description,
      price: props.plan.price,
      original_price: props.plan.original_price || 0,
      currency: props.plan.currency || '',
      validity_days: props.plan.validity_days,
      validity_unit: props.plan.validity_unit || 'days',
      daily_limit_usd: props.plan.daily_limit_usd ?? null,
      weekly_limit_usd: props.plan.weekly_limit_usd ?? null,
      monthly_limit_usd: props.plan.monthly_limit_usd ?? null,
      sort_order: props.plan.sort_order || 0,
      for_sale: props.plan.for_sale,
    })
    planFeaturesText.value = (props.plan.features || []).join('\n')
  } else {
    Object.assign(planForm, {
      sale_schedule: { ends_at: null, price_steps: [], daily_windows: [] },
      card_mode: '',
      validity_hours: 24,
      deadline_at: '',
      total_limit_usd: null,
      name: '',
      description: '',
      price: 0,
      original_price: 0,
      currency: '',
      validity_days: 30,
      validity_unit: 'days',
      daily_limit_usd: null,
      weekly_limit_usd: null,
      monthly_limit_usd: null,
      sort_order: 0,
      for_sale: true,
    })
    planFeaturesText.value = ''
  }
})

/** Build request payload with snake_case keys matching backend JSON tags */
function buildPlanPayload() {
  const features = planFeaturesText.value.split('\n').map(f => f.trim()).filter(Boolean).join('\n')
  const normalizeLimit = (value: unknown): number | null => {
    if (value === '' || value == null) return null
    const parsed = Number(value)
    return Number.isFinite(parsed) ? parsed : null
  }
  return {
    sale_schedule: planForm.card_mode ? { ...planForm.sale_schedule, daily_windows: planForm.card_mode === 'hours' ? planForm.sale_schedule.daily_windows : [] } : { ends_at: null, price_steps: [], daily_windows: [] },
    card_mode: planForm.card_mode,
    validity_hours: planForm.card_mode === 'hours' ? planForm.validity_hours : 0,
    deadline_at: planForm.card_mode === 'deadline' ? new Date(planForm.deadline_at).toISOString() : null,
    total_limit_usd: planForm.card_mode ? normalizeLimit(planForm.total_limit_usd) : null,
    name: planForm.name,
    description: planForm.description,
    price: planForm.price,
    original_price: planForm.original_price || 0,
    currency: planForm.currency.trim().toUpperCase(),
    validity_days: planForm.validity_days,
    validity_unit: planForm.validity_unit,
    daily_limit_usd: planForm.card_mode ? null : normalizeLimit(planForm.daily_limit_usd),
    weekly_limit_usd: planForm.card_mode ? null : normalizeLimit(planForm.weekly_limit_usd),
    monthly_limit_usd: planForm.card_mode ? null : normalizeLimit(planForm.monthly_limit_usd),
    sort_order: planForm.sort_order,
    for_sale: planForm.for_sale,
    features,
  }
}

async function handleSavePlan() {
  if (!planForm.price || planForm.price <= 0) {
    appStore.showError(t('payment.admin.priceRequired'))
    return
  }
  if (!planForm.validity_days || planForm.validity_days < 1) {
    appStore.showError(t('payment.admin.validityRequired'))
    return
  }
  saving.value = true
  try {
    const data = buildPlanPayload()
    if (props.plan) { await adminPaymentAPI.updatePlan(props.plan.id, data) }
    else { await adminPaymentAPI.createPlan(data) }
    appStore.showSuccess(t('common.saved'))
    emit('close')
    emit('saved')
  } catch (err: unknown) { appStore.showError(extractApiErrorMessage(err, t('common.error'))) }
  finally { saving.value = false }
}

function toLocalDateTime(value?: string | null): string {
  if (!value) return ''
  const date = new Date(value)
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16)
}
</script>
