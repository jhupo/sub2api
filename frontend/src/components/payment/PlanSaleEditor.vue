<template>
  <fieldset class="space-y-4 border-t border-gray-200 pt-4 dark:border-dark-600">
    <legend class="px-1 text-sm font-semibold">{{ t('quotaFeatures.saleSchedule') }}</legend>
    <label class="block">
      <span class="input-label">{{ t('quotaFeatures.saleEnd') }}</span>
      <input :value="localTime(modelValue.ends_at)" type="datetime-local" class="input" :placeholder="t('quotaFeatures.saleEnd')" @input="setEnd(($event.target as HTMLInputElement).value)" />
      <span v-if="mode === 'deadline'" class="mt-1 block text-xs text-gray-500">{{ t('quotaFeatures.defaultSaleEnd') }}</span>
    </label>
    <div class="space-y-2">
      <div class="flex items-center justify-between gap-2">
        <span class="text-sm font-medium">{{ t('quotaFeatures.priceSteps') }}</span>
        <button type="button" class="btn btn-secondary btn-sm" @click="addPrice"><Icon name="plus" size="sm" />{{ t('common.add') }}</button>
      </div>
      <div v-for="(step, index) in modelValue.price_steps" :key="index" class="grid grid-cols-[minmax(0,1fr)_minmax(70px,100px)_36px] items-end gap-2">
        <label class="min-w-0"><span class="input-label">{{ t('quotaFeatures.effectiveAt') }}</span><input :value="localTime(step.at)" type="datetime-local" class="input min-w-0" required @input="updateStep(index, { at: isoTime(($event.target as HTMLInputElement).value) })" /></label>
        <label><span class="input-label">{{ t('payment.admin.price') }}</span><input :value="step.price" type="number" min="0.01" step="0.01" class="input" required @input="updateStep(index, { price: Number(($event.target as HTMLInputElement).value) })" /></label>
        <button type="button" class="btn btn-secondary h-10 w-9 px-0" :title="t('common.delete')" :aria-label="t('common.delete')" @click="removePrice(index)"><Icon name="trash" size="sm" /></button>
      </div>
    </div>
    <div v-if="mode === 'hours'" class="space-y-2">
      <div class="flex items-center justify-between gap-2">
        <span class="text-sm font-medium">{{ t('quotaFeatures.dailySaleWindows') }} <span class="text-xs text-gray-500">{{ timezone }}</span></span>
        <button type="button" class="btn btn-secondary btn-sm" @click="addWindow"><Icon name="plus" size="sm" />{{ t('common.add') }}</button>
      </div>
      <div v-for="(window, index) in modelValue.daily_windows" :key="index" class="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_36px] items-end gap-2">
        <label class="min-w-0"><span class="input-label">{{ t('quotaFeatures.start') }}</span><input :value="window.start" type="time" class="input min-w-0" required @input="updateWindow(index, { start: ($event.target as HTMLInputElement).value })" /></label>
        <label class="min-w-0"><span class="input-label">{{ t('quotaFeatures.end') }}<span v-if="window.end < window.start"> · {{ t('quotaFeatures.nextDay') }}</span></span><input :value="window.end" type="time" class="input min-w-0" required @input="updateWindow(index, { end: ($event.target as HTMLInputElement).value })" /></label>
        <button type="button" class="btn btn-secondary h-10 w-9 px-0" :title="t('common.delete')" :aria-label="t('common.delete')" @click="removeWindow(index)"><Icon name="trash" size="sm" /></button>
      </div>
    </div>
  </fieldset>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { PlanSaleSchedule } from '@/types/payment'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{ modelValue: PlanSaleSchedule; mode: 'hours' | 'deadline'; timezone: string }>()
const emit = defineEmits<{ 'update:modelValue': [value: PlanSaleSchedule] }>()
const { t } = useI18n()
function localTime(value?: string | null) {
  if (!value) return ''
  const date = new Date(value)
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16)
}
function isoTime(value: string) { return value ? new Date(value).toISOString() : '' }
function setEnd(value: string) { emit('update:modelValue', { ...props.modelValue, ends_at: value ? isoTime(value) : null }) }
function addPrice() { emit('update:modelValue', { ...props.modelValue, price_steps: [...props.modelValue.price_steps, { at: '', price: 0 }] }) }
function updateStep(index: number, patch: Partial<PlanSaleSchedule['price_steps'][number]>) {
  emit('update:modelValue', { ...props.modelValue, price_steps: props.modelValue.price_steps.map((step, i) => i === index ? { ...step, ...patch } : step) })
}
function removePrice(index: number) { emit('update:modelValue', { ...props.modelValue, price_steps: props.modelValue.price_steps.filter((_, i) => i !== index) }) }
function updateWindow(index: number, patch: Partial<PlanSaleSchedule['daily_windows'][number]>) {
  emit('update:modelValue', { ...props.modelValue, daily_windows: props.modelValue.daily_windows.map((window, i) => i === index ? { ...window, ...patch } : window) })
}
function addWindow() { emit('update:modelValue', { ...props.modelValue, daily_windows: [...props.modelValue.daily_windows, { start: '22:00', end: '08:00' }] }) }
function removeWindow(index: number) { emit('update:modelValue', { ...props.modelValue, daily_windows: props.modelValue.daily_windows.filter((_, i) => i !== index) }) }
</script>
