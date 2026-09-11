<script setup lang="ts">
import { computed } from 'vue'
import type { AccountUsageInfo } from '@/types'
import UsageProgressBar from './UsageProgressBar.vue'

const props = defineProps<{ usage: AccountUsageInfo }>()
const groups = computed(() => (props.usage.antigravity_quota_groups ?? []).map(group => ({
  ...group,
  buckets: group.buckets.filter(bucket => typeof bucket.remainingFraction === 'number' &&
    Number.isFinite(bucket.remainingFraction) && bucket.remainingFraction >= 0 && bucket.remainingFraction <= 1)
})).filter(group => group.buckets.length > 0))
const models = computed(() => Object.entries(props.usage.antigravity_quota ?? {}).sort(([a], [b]) => a.localeCompare(b)))
</script>

<template>
  <div class="space-y-1">
    <div v-for="(group, index) in groups" :key="index" class="space-y-1">
      <div class="text-[10px] text-gray-500 break-words">{{ group.displayName }}</div>
      <div v-for="(bucket, bucketIndex) in group.buckets" :key="`${bucket.bucketId}:${bucketIndex}`" class="space-y-1">
        <div class="text-[10px] text-gray-500 break-words">{{ bucket.displayName || bucket.window || bucket.bucketId }}</div>
        <UsageProgressBar
          label=""
          :utilization="(1 - bucket.remainingFraction!) * 100"
          :resets-at="bucket.resetTime || null"
          color="emerald"
        />
      </div>
    </div>
    <template v-if="groups.length === 0">
      <div v-for="[model, quota] in models" :key="model" class="space-y-1">
        <div class="text-[10px] text-gray-500 break-words">{{ model }}</div>
        <UsageProgressBar label="" :utilization="quota.utilization" :resets-at="quota.reset_time || null" color="emerald" />
      </div>
    </template>
  </div>
</template>
