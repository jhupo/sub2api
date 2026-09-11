<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountUsageInfo } from '@/types'
import UsageProgressBar from './UsageProgressBar.vue'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{ usage: AccountUsageInfo; accountLabel?: string }>()
const { t } = useI18n()
const key = 'admin.accounts.gemini.compactQuota'
const valid = (value: unknown): value is number =>
  typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= 1
const percent = (value: number | null) => value === null ? '—' : `${Math.round(value * 100)}%`
const date = (value?: string | null) => {
  if (!value) return '—'
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? '—' : parsed.toLocaleString()
}
const windowLabel = (window: string, fallback: string) => {
  const normalized = window.toLowerCase().replace(/[\s_-]/g, '')
  if (['5h', 'fivehour', 'fivehours'].includes(normalized)) return '5h'
  if (['7d', 'weekly', 'week', 'sevenday'].includes(normalized)) return t(`${key}.week`)
  return window || fallback
}
const rows = computed(() => {
  const groups = props.usage.antigravity_quota_groups ?? []
  if (groups.length) return groups.map((group, index) => {
    const buckets = group.buckets.map(bucket => ({
      label: windowLabel(bucket.window, bucket.displayName || bucket.bucketId),
      remaining: valid(bucket.remainingFraction) ? bucket.remainingFraction : null,
      reset: bucket.resetTime
    }))
    // Only compare windows within a group explicitly supplied by the upstream.
    const limiting = buckets.filter(bucket => bucket.remaining !== null)
      .reduce<(typeof buckets)[number] | null>((min, bucket) =>
        !min || bucket.remaining! < min.remaining! ? bucket : min, null)
    const label = group.displayName === 'Gemini Models' ? 'Gemini'
      : group.displayName === 'Claude and GPT models' ? 'Claude / GPT' : group.displayName
    return { id: `group-${index}`, label, buckets, limiting }
  })
  return Object.entries(props.usage.antigravity_quota ?? {}).sort(([a], [b]) => a.localeCompare(b))
    .map(([label, quota]) => {
      const remaining = typeof quota.utilization === 'number' && valid(quota.utilization / 100)
        ? 1 - quota.utilization / 100 : null
      const bucket = { label: '', remaining, reset: quota.reset_time }
      return { id: label, label, buckets: [bucket], limiting: remaining === null ? null : bucket }
    })
})
const hoverText = computed(() => [t(`${key}.remaining`), ...rows.value.flatMap(row =>
  row.buckets.map(bucket => `${row.label} ${bucket.label}: ${percent(bucket.remaining)} · ${t(`${key}.reset`)} ${date(bucket.reset)}`)
), `${t(`${key}.updated`)} ${date(props.usage.updated_at)}`].join('\n'))
</script>

<template>
  <details class="gemini-quota group text-xs text-gray-600 dark:text-gray-400">
    <summary
      class="relative cursor-pointer list-none rounded pr-4 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500"
      :title="hoverText"
      :aria-label="t(`${key}.details`)"
    >
      <Icon name="chevronDown" class="absolute right-0 top-1 h-3 w-3 text-gray-400 group-open:rotate-180" />
      <div v-for="row in rows.slice(0, 2)" :key="row.id" class="flex h-6 items-center gap-1" data-testid="quota-summary-row">
        <span class="w-20 shrink-0 truncate text-[10px]" :title="row.label">{{ row.label }}</span>
        <UsageProgressBar
          v-if="row.limiting"
          label=""
          hide-label
          :utilization="row.limiting.remaining! * 100"
          remaining-capacity
          color="emerald"
          class="quota-bar"
        />
        <span v-else class="w-[72px] text-center text-gray-400">—</span>
        <span class="max-w-12 truncate text-[10px]">{{ row.limiting?.label }}</span>
      </div>
      <span v-if="rows.length > 2" class="text-[10px] text-gray-400">+{{ rows.length - 2 }}</span>
    </summary>
    <div class="mt-2 max-w-72 space-y-2 border-t border-gray-200 pt-2 dark:border-gray-700">
      <div v-if="accountLabel" class="text-[10px]">{{ accountLabel }}</div>
      <div class="text-[10px] font-medium">{{ t(`${key}.remaining`) }}</div>
      <div v-for="row in rows" :key="row.id" class="space-y-1">
        <div class="break-words font-medium">{{ row.label }}</div>
        <div v-for="(bucket, index) in row.buckets" :key="index" class="text-[10px]">
          <div class="flex justify-between gap-3"><span>{{ bucket.label }}</span><span>{{ percent(bucket.remaining) }}</span></div>
          <div class="text-gray-400">{{ t(`${key}.reset`) }} {{ date(bucket.reset) }}</div>
        </div>
      </div>
      <div class="text-[10px] text-gray-400">{{ t(`${key}.updated`) }} {{ date(usage.updated_at) }}</div>
      <slot />
    </div>
  </details>
</template>

<style scoped>
summary::-webkit-details-marker { display: none; }
.quota-bar :deep(.flex-wrap) { flex-wrap: nowrap; }
</style>
