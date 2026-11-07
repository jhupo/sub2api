<template>
  <div class="space-y-4 border-t pt-4">
    <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
      <input v-model="form.night_rate_enabled" type="checkbox" class="rounded border-gray-300" />
      {{ t('quotaFeatures.nightEnabled') }}
    </label>
    <div v-if="form.night_rate_enabled" class="grid grid-cols-1 gap-3 sm:grid-cols-3">
      <label class="block"><span class="input-label">{{ t('quotaFeatures.start') }}</span><input v-model="form.night_start" type="time" class="input" required /></label>
      <label class="block"><span class="input-label">{{ t('quotaFeatures.end') }}{{ form.night_start > form.night_end ? ` (${t('quotaFeatures.nextDay')})` : '' }}</span><input v-model="form.night_end" type="time" class="input" required /></label>
      <label class="block"><span class="input-label">{{ t('quotaFeatures.multiplier') }}</span><input v-model.number="form.night_rate_multiplier" type="number" min="0" step="0.001" class="input" required /></label>
    </div>
    <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
      <label class="block">
        <span class="input-label">{{ t('quotaFeatures.cacheMode') }}</span>
        <select v-model="form.cache_read_adjustment_mode" class="input">
          <option value="inherit">{{ t('quotaFeatures.cacheInherit') }}</option>
          <option value="custom">{{ t('quotaFeatures.cacheCustom') }}</option>
          <option value="disabled">{{ t('quotaFeatures.cacheDisabled') }}</option>
        </select>
      </label>
      <label v-if="form.cache_read_adjustment_mode === 'custom'" class="block">
        <span class="input-label">{{ t('quotaFeatures.cachePercent') }}</span>
        <input v-model.number="form.cache_read_adjustment_percent" type="number" min="0" max="100" step="1" class="input" required />
      </label>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
const form = defineModel<{
  night_rate_enabled: boolean
  night_start: string
  night_end: string
  night_rate_multiplier: number
  cache_read_adjustment_mode: string
  cache_read_adjustment_percent: number
}>({ required: true })
const { t } = useI18n()
</script>
