<template>
  <div class="guide-markdown" @click="navigate">
    <template v-for="(block, index) in blocks" :key="index">
      <GuideCodeBlock v-if="block.kind === 'code'" :code="block.code" :label="block.language" />
      <!-- HTML is parsed with marked and sanitized before rendering. -->
      <!-- eslint-disable-next-line vue/no-v-html -->
      <div v-else class="guide-markdown-html" v-html="block.html"></div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import GuideCodeBlock from './GuideCodeBlock.vue'
import { getGuideMarkdownBlocks, type GuideVariables } from '@/utils/guideMarkdown'

const props = defineProps<{ markdown: string; variables: GuideVariables }>()
const router = useRouter()
const blocks = computed(() => getGuideMarkdownBlocks(props.markdown, props.variables))

function navigate(event: MouseEvent) {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  if (!(event.target instanceof Element)) return
  const link = event.target.closest('a')
  const href = link?.getAttribute('href') || ''
  if ((href.startsWith('/') && !href.startsWith('//')) || href.startsWith('#')) {
    event.preventDefault()
    void router.push(href)
  }
}
</script>

<style scoped>
.guide-markdown { display: grid; gap: 16px; min-width: 0; color: var(--guide-text, #1f2937); font-size: 14px; line-height: 1.9; overflow-wrap: anywhere; }
.dark .guide-markdown { color: var(--guide-text, #e2e8f0); }
.guide-markdown-html { min-width: 0; }
.guide-markdown-html :deep(> :first-child) { margin-top: 0; }
.guide-markdown-html :deep(> :last-child) { margin-bottom: 0; }
.guide-markdown-html :deep(p), .guide-markdown-html :deep(ul), .guide-markdown-html :deep(ol) { margin: 12px 0; }
.guide-markdown-html :deep(h1), .guide-markdown-html :deep(h2) { margin: 26px 0 12px; font-size: 18px; font-weight: 650; line-height: 1.6; }
.guide-markdown-html :deep(h3), .guide-markdown-html :deep(h4), .guide-markdown-html :deep(h5), .guide-markdown-html :deep(h6) { margin: 22px 0 10px; font-size: 15px; font-weight: 600; line-height: 1.7; }
.guide-markdown-html :deep(ul) { padding-left: 22px; list-style: disc; }
.guide-markdown-html :deep(ol) { padding-left: 22px; list-style: decimal; }
.guide-markdown-html :deep(li) { margin: 5px 0; padding-left: 3px; }
.guide-markdown-html :deep(li::marker) { color: #059669; }
.guide-markdown-html :deep(a) { color: #2563eb; text-decoration: underline; text-underline-offset: 3px; }
.dark .guide-markdown-html :deep(a) { color: #93c5fd; }
.guide-markdown-html :deep(code) { padding: 2px 5px; border-radius: 3px; background: var(--guide-surface, #f3f4f6); font-size: 12px; }
.guide-markdown-html :deep(pre) { max-width: 100%; overflow: auto; padding: 16px; border-radius: 6px; background: #202226; color: #e5e7eb; }
.guide-markdown-html :deep(pre code) { padding: 0; background: transparent; }
.dark .guide-markdown-html :deep(code) { background: var(--guide-surface, #192331); }
.guide-markdown-html :deep(blockquote) { margin: 16px 0; padding: 10px 14px; border-left: 3px solid #34d399; background: var(--guide-surface, #f8fafb); font-size: 13px; }
.guide-markdown-html :deep(blockquote > :first-child) { margin-top: 0; }
.guide-markdown-html :deep(blockquote > :last-child) { margin-bottom: 0; }
.dark .guide-markdown-html :deep(blockquote) { background: var(--guide-surface, #192331); }
.guide-markdown-html :deep(.guide-markdown-table) { max-width: 100%; margin: 16px 0; overflow-x: auto; border: 1px solid var(--guide-line, #e5e7eb); border-radius: 6px; }
.guide-markdown-html :deep(table) { width: 100%; border-collapse: collapse; font-size: 13px; text-align: left; }
.guide-markdown-html :deep(th), .guide-markdown-html :deep(td) { padding: 10px 12px; border-bottom: 1px solid var(--guide-line, #e5e7eb); vertical-align: top; }
.guide-markdown-html :deep(th) { background: var(--guide-surface, #f8fafb); font-weight: 500; }
.guide-markdown-html :deep(tr:last-child td) { border-bottom: 0; }
.dark .guide-markdown-html :deep(.guide-markdown-table), .dark .guide-markdown-html :deep(th), .dark .guide-markdown-html :deep(td) { border-color: var(--guide-line, #334155); }
.dark .guide-markdown-html :deep(th) { background: var(--guide-surface, #192331); }
.guide-markdown-html :deep(img) { display: block; max-width: 100%; height: auto; margin: 16px 0; border-radius: 6px; }
.guide-markdown-html :deep(hr) { margin: 24px 0; border: 0; border-top: 1px solid var(--guide-line, #e5e7eb); }
.guide-markdown-html :deep(input[type='checkbox']) { accent-color: #059669; }
.guide-markdown-html :deep(:is(a, summary, [tabindex]):focus-visible) { outline: 2px solid #60a5fa; outline-offset: 3px; }
</style>
