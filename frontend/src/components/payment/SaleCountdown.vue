<template>
  <div v-if="endsAt" class="flex flex-wrap items-center justify-between gap-2 text-sm" :title="formatDateTimeToMinute(endsAt)">
    <span class="text-gray-500 dark:text-gray-400">{{ t('quotaFeatures.saleCountdown') }}</span>
    <time :datetime="endsAt" class="font-medium tabular-nums text-rose-600 dark:text-rose-400">{{ seconds > 0 ? countdown : t('quotaFeatures.offSale') }}</time>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatDateTimeToMinute } from '@/utils/format'
const props = defineProps<{ endsAt?: string | null }>()
const emit = defineEmits<{ expired: [] }>()
const { t } = useI18n()
const now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | undefined
const seconds = computed(() => props.endsAt ? Math.max(0, Math.ceil((new Date(props.endsAt).getTime() - now.value) / 1000)) : 0)
const countdown = computed(() => {
  const days = Math.floor(seconds.value / 86400)
  const hours = String(Math.floor(seconds.value % 86400 / 3600)).padStart(2, '0')
  const minutes = String(Math.floor(seconds.value % 3600 / 60)).padStart(2, '0')
  const secs = String(seconds.value % 60).padStart(2, '0')
  return `${days ? t('quotaFeatures.countdownDays', { days }) + ' ' : ''}${hours}:${minutes}:${secs}`
})
watch(seconds, (value, previous) => { if (value === 0 && previous > 0) emit('expired') })
onMounted(() => { timer = setInterval(() => { now.value = Date.now() }, 1000) })
onBeforeUnmount(() => { if (timer) clearInterval(timer) })
</script>
