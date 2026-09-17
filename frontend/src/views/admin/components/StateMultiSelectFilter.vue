<template>
  <details class="state-filter relative" :data-testid="testId">
    <summary class="input flex h-10 min-w-0 cursor-pointer select-none items-center justify-between gap-2 whitespace-nowrap px-3 text-sm sm:min-w-[10rem]">
      <span class="truncate">{{ summary }}</span>
      <span aria-hidden="true" class="text-[10px] text-gray-400">▼</span>
    </summary>
    <div class="absolute left-0 z-40 mt-2 w-[min(18rem,calc(100vw-2rem))] rounded-2xl border border-gray-200 bg-white p-2 shadow-xl dark:border-dark-700 dark:bg-dark-800">
      <div class="max-h-64 space-y-1 overflow-y-auto">
        <label v-for="option in options" :key="option.value" class="flex cursor-pointer items-start gap-2 rounded-xl px-2.5 py-2 hover:bg-gray-50 dark:hover:bg-dark-700">
          <input
            type="checkbox"
            class="mt-0.5 h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-900"
            :checked="modelValue.includes(option.value)"
            @change="toggle(option.value)"
          />
          <span class="min-w-0">
            <span class="block truncate text-sm font-medium text-gray-800 dark:text-gray-100">{{ option.label }}</span>
            <span v-if="option.description" class="block truncate text-[11px] text-gray-500 dark:text-gray-400">{{ option.description }}</span>
          </span>
        </label>
      </div>
      <div class="mt-2 flex items-center justify-between border-t border-gray-100 px-2 pt-2 text-xs dark:border-dark-700">
        <button type="button" class="text-gray-500 hover:text-gray-900 dark:text-gray-400 dark:hover:text-white" @click="emit('update:modelValue', [])">{{ clearLabel }}</button>
        <button type="button" class="text-primary-600 hover:underline dark:text-primary-400" @click="emit('update:modelValue', options.map(option => option.value))">{{ allLabel }}</button>
      </div>
    </div>
  </details>
</template>

<script setup lang="ts">
import { computed } from 'vue'

interface FilterOption {
  value: string
  label: string
  description?: string
}

const props = defineProps<{
  modelValue: string[]
  options: FilterOption[]
  placeholder: string
  selectedLabel: string
  clearLabel: string
  allLabel: string
  testId?: string
}>()
const emit = defineEmits<{ 'update:modelValue': [value: string[]] }>()

const summary = computed(() => props.modelValue.length
  ? props.selectedLabel.replace('{count}', String(props.modelValue.length))
  : props.placeholder)

function toggle(value: string) {
  const selected = new Set(props.modelValue)
  if (selected.has(value)) selected.delete(value)
  else selected.add(value)
  emit('update:modelValue', [...selected])
}
</script>

<style scoped>
.state-filter > summary {
  list-style: none;
}

.state-filter > summary::-webkit-details-marker {
  display: none;
}
</style>
