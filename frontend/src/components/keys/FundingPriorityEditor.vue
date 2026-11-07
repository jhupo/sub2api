<template>
  <div class="space-y-3">
    <label class="input-label">{{ t('quotaFeatures.priority') }}</label>
    <ol class="divide-y divide-gray-200 border-y border-gray-200 dark:divide-dark-600 dark:border-dark-600">
      <li v-for="(id, index) in modelValue" :key="id" class="flex min-h-14 items-center gap-3 py-2">
        <span class="w-5 shrink-0 text-center text-sm font-semibold text-gray-500">{{ index + 1 }}</span>
        <div class="min-w-0 flex-1 text-sm">
          <div class="break-words">{{ findSubscription(id)?.plan?.name }} <span class="text-xs text-gray-500">#{{ id }}</span></div>
          <div v-if="findSubscription(id)" class="text-xs text-gray-500">{{ new Date(findSubscription(id)!.expires_at).toLocaleString() }}</div>
        </div>
        <button type="button" class="btn-icon h-8 w-8 shrink-0" :title="t('quotaFeatures.moveUp')" :aria-label="t('quotaFeatures.moveUp')" :disabled="index === 0" @click="move(index, -1)"><Icon name="arrowUp" size="sm" /></button>
        <button type="button" class="btn-icon h-8 w-8 shrink-0" :title="t('quotaFeatures.moveDown')" :aria-label="t('quotaFeatures.moveDown')" :disabled="index === modelValue.length - 1" @click="move(index, 1)"><Icon name="arrowDown" size="sm" /></button>
        <button type="button" class="btn-icon h-8 w-8 shrink-0 text-red-600" :title="t('quotaFeatures.remove')" :aria-label="t('quotaFeatures.remove')" :disabled="modelValue.length === 1" @click="emit('update:modelValue', modelValue.filter(value => value !== id))"><Icon name="trash" size="sm" /></button>
      </li>
    </ol>
    <div class="flex gap-2">
      <select v-model="nextID" class="input min-w-0 flex-1" :aria-label="t('quotaFeatures.addCard')">
        <option :value="0">{{ t('quotaFeatures.addCard') }}</option>
        <option v-for="sub in available" :key="sub.id" :value="sub.id">{{ sub.plan?.name || `#${sub.id}` }} #{{ sub.id }}</option>
      </select>
      <button type="button" class="btn btn-secondary shrink-0" :disabled="!nextID || modelValue.length >= 32" @click="add"><Icon name="plus" size="sm" />{{ t('common.add') }}</button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { UserSubscription } from '@/types'
const props = defineProps<{ modelValue: number[]; subscriptions: UserSubscription[] }>()
const emit = defineEmits<{ 'update:modelValue': [ids: number[]] }>()
const { t } = useI18n()
const nextID = ref(0)
const available = computed(() => props.subscriptions.filter(sub => !props.modelValue.includes(sub.id)))
function findSubscription(id: number) { return props.subscriptions.find(sub => sub.id === id) }
function add() {
  if (!nextID.value || props.modelValue.includes(nextID.value)) return
  emit('update:modelValue', [...props.modelValue, nextID.value])
  nextID.value = 0
}
function move(index: number, offset: number) {
  const next = [...props.modelValue]
  const other = index + offset
  if (other < 0 || other >= next.length) return
  ;[next[index], next[other]] = [next[other]!, next[index]!]
  emit('update:modelValue', next)
}
</script>
