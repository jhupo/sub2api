<template>
  <div class="guide-code">
    <div class="guide-code-header">
      <span class="min-w-0 break-all font-mono">{{ label }}</span>
      <button
        type="button"
        class="guide-copy"
        :title="copied ? t('common.copied') : t('common.copy')"
        :aria-label="`${copied ? t('common.copied') : t('common.copy')}: ${label}`"
        @click="copyToClipboard(code)"
      >
        <Icon :name="copied ? 'check' : 'copy'" size="sm" />
      </button>
    </div>
    <pre tabindex="0"><code>{{ code }}</code></pre>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useClipboard } from '@/composables/useClipboard'

defineProps<{ code: string; label: string }>()
const { t } = useI18n()
const { copied, copyToClipboard } = useClipboard()
</script>

<style scoped>
.guide-code { overflow: hidden; border: 1px solid #383b40; border-radius: 8px; background: #202226; color: #e5e7eb; }
.guide-code-header { display: flex; min-height: 42px; align-items: center; justify-content: space-between; gap: 12px; border-bottom: 1px solid #383b40; padding: 4px 12px 4px 16px; color: #b4b9c1; font-size: 12px; }
.guide-copy { display: flex; width: 32px; height: 32px; flex-shrink: 0; align-items: center; justify-content: center; border-radius: 4px; }
.guide-copy:hover { background: #383b40; color: #fff; }
pre { margin: 0; max-width: 100%; overflow-x: auto; padding: 18px; font-size: 12px; line-height: 1.9; tab-size: 2; }
button:focus-visible, pre:focus-visible { outline: 2px solid #60a5fa; outline-offset: -3px; }
</style>
