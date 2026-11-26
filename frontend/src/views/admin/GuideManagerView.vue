<template>
  <AppLayout>
    <div class="guide-manager">
      <header class="manager-toolbar">
        <div class="manager-status" role="status">
          <span :class="{ changed: dirty || unpublished }">{{ t(dirty ? 'guideManager.unsaved' : unpublished ? 'guideManager.unpublished' : 'guideManager.published') }}</span>
          <small>{{ document?.published_at ? t('guideManager.publishedAt', { time: formatDateTimeToMinute(document.published_at) }) : t('guideManager.neverPublished') }}</small>
        </div>
        <div class="manager-actions">
          <RouterLink :to="`/guide#${chapter}`" target="_blank" class="btn btn-secondary" :title="t('guideManager.viewGuide')"><Icon name="externalLink" size="sm" /><span class="action-label">{{ t('guideManager.viewGuide') }}</span></RouterLink>
          <button type="button" class="btn btn-secondary" :disabled="loading || saving" :title="t('guideManager.reload')" :aria-label="t('guideManager.reload')" @click="requestReload"><Icon name="refresh" size="sm" /></button>
          <button type="button" class="btn btn-secondary" :disabled="!ready || saving || !dirty || invalid" @click="save(false)"><Icon name="document" size="sm" />{{ t('guideManager.saveDraft') }}</button>
          <button type="button" class="btn btn-primary" :disabled="!ready || saving || !unpublished || invalid" @click="save(true)"><Icon name="upload" size="sm" />{{ t('guideManager.publish') }}</button>
        </div>
      </header>

      <div v-if="error" class="manager-error" role="alert">{{ error }}</div>
      <div v-if="loading" class="py-12 text-center text-sm text-gray-500">{{ t('common.loading') }}</div>
      <fieldset v-else-if="ready" class="manager-workspace" :disabled="saving">
        <aside class="manager-chapters">
          <label class="input-label" for="guide-language">{{ t('guideManager.language') }}</label>
          <select id="guide-language" v-model="language" class="input"><option value="zh">中文</option><option value="en">English</option></select>
          <nav :aria-label="t('guideManager.chapter')">
            <button v-for="(item, index) in guideChapters" :key="item.id" type="button" :class="{ active: chapter === item.id }" :aria-current="chapter === item.id ? 'page' : undefined" @click="chapter = item.id">
              <span class="chapter-number">{{ String(index + 1).padStart(2, '0') }}</span>
              <span>{{ chapterFor(item.id).title }}</span>
              <span v-if="draft[language]?.[item.id]" class="custom-dot" :title="t('guideManager.custom')"></span>
            </button>
          </nav>
          <label class="input-label mt-5" for="guide-template-os">{{ t('guideManager.templateOS') }}</label>
          <select id="guide-template-os" v-model="os" class="input"><option value="unix">{{ t('guideManager.unix') }}</option><option value="windows">{{ t('guideManager.windows') }}</option></select>
        </aside>

        <div class="manager-main">
          <div class="manager-mobile-selects">
            <label>{{ t('guideManager.language') }}<select v-model="language" class="input"><option value="zh">中文</option><option value="en">English</option></select></label>
            <label>{{ t('guideManager.chapter') }}<select v-model="chapter" class="input"><option v-for="item in guideChapters" :key="item.id" :value="item.id">{{ chapterFor(item.id).title }}</option></select></label>
            <label class="mobile-template-os">{{ t('guideManager.templateOS') }}<select v-model="os" class="input"><option value="unix">{{ t('guideManager.unix') }}</option><option value="windows">{{ t('guideManager.windows') }}</option></select></label>
          </div>
          <div class="manager-fields">
            <label class="min-w-0"><span class="input-label">{{ t('guideManager.titleLabel') }}</span><input :value="current.title" class="input" maxlength="120" @input="updateChapter({ title: ($event.target as HTMLInputElement).value })" /></label>
            <label class="min-w-0"><span class="input-label">{{ t('guideManager.descriptionLabel') }}</span><input :value="current.description" class="input" maxlength="400" @input="updateChapter({ description: ($event.target as HTMLInputElement).value })" /></label>
          </div>
          <div class="manager-content-bar">
            <span class="text-xs text-gray-500">{{ t(draft[language]?.[chapter] ? 'guideManager.custom' : 'guideManager.default') }}</span>
            <div class="manager-mode" role="tablist" :aria-label="t('guideManager.bodyLabel')">
              <button v-for="item in ['edit', 'preview'] as const" :key="item" type="button" role="tab" :aria-selected="mode === item" :class="{ active: mode === item }" @click="mode = item">{{ t(`guideManager.${item}`) }}</button>
            </div>
            <button type="button" class="manager-icon-button ml-auto" :disabled="!draft[language]?.[chapter] || saving" :title="t('guideManager.restore')" :aria-label="t('guideManager.restore')" @click="confirmation = 'restore'"><Icon name="refresh" size="sm" /></button>
          </div>
          <p v-if="invalid" class="manager-error" role="alert">{{ t('guideManager.invalid') }}</p>

          <div class="manager-editors" :class="`mode-${mode}`">
            <section class="manager-editor" :aria-label="t('guideManager.edit')">
              <div class="markdown-toolbar">
                <button v-for="tool in ['bold', 'italic', 'heading'] as const" :key="tool" type="button" class="manager-icon-button" :title="t(`guideManager.${tool}`)" :aria-label="t(`guideManager.${tool}`)" @click="format(tool)"><b v-if="tool === 'bold'">B</b><i v-else-if="tool === 'italic'">I</i><span v-else>H2</span></button>
                <select class="manager-tool-select" :aria-label="t('guideManager.insertBlock')" value="" @change="insertBlock(($event.target as HTMLSelectElement).value); ($event.target as HTMLSelectElement).value = ''">
                  <option value="" disabled>{{ t('guideManager.insertBlock') }}</option>
                  <option v-for="tool in ['bulletList', 'numberedList', 'quote', 'code', 'table', 'image', 'link']" :key="tool" :value="tool">{{ t(`guideManager.${tool}`) }}</option>
                </select>
                <select class="manager-tool-select manager-variable-select" :aria-label="t('guideManager.variables')" value="" @change="insertText('{{' + ($event.target as HTMLSelectElement).value + '}}'); ($event.target as HTMLSelectElement).value = ''">
                  <option value="" disabled>{{ t('guideManager.variables') }}</option>
                  <option v-for="variable in guidePlaceholders" :key="variable" :value="variable">{{ t(`guideManager.variableLabels.${variable}`) }}</option>
                </select>
              </div>
              <textarea ref="textarea" :value="current.markdown" :aria-label="t('guideManager.bodyLabel')" spellcheck="false" @input="updateChapter({ markdown: ($event.target as HTMLTextAreaElement).value })"></textarea>
            </section>
            <section class="manager-preview" :aria-label="t('guideManager.preview')">
              <div class="preview-chapter-heading"><span>{{ t('beginnerGuide.chapter', { number: String(guideChapters.findIndex(item => item.id === chapter) + 1).padStart(2, '0') }) }}</span><h2>{{ current.title }}</h2><p>{{ current.description }}</p></div>
              <GuideMarkdown :markdown="current.markdown" :variables="variables" />
            </section>
          </div>
        </div>
      </fieldset>
    </div>
    <ConfirmDialog :show="confirmation !== null" :title="t(confirmation === 'restore' ? 'guideManager.restoreTitle' : 'guideManager.reloadTitle')" :message="t(confirmation === 'restore' ? 'guideManager.restoreMessage' : 'guideManager.reloadMessage')" @cancel="confirmation = null" @confirm="confirmAction" />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import GuideMarkdown from '@/components/Guide/GuideMarkdown.vue'
import { beginnerGuideAPI, type GuideChapterContent, type GuideContent, type GuideDocument, type GuideLanguage } from '@/api/beginnerGuide'
import { getGuideEndpoints, guideChapters, type GuideChapterId, type GuideOS } from '@/utils/beginnerGuide'
import { getDefaultGuideChapter } from '@/utils/guideDefaults'
import { guidePlaceholders } from '@/utils/guideMarkdown'
import { formatDateTimeToMinute } from '@/utils/format'
import { useAppStore } from '@/stores/app'
import { loadLocaleMessages } from '@/i18n'

const { t, locale } = useI18n()
const appStore = useAppStore()
const loading = ref(true)
const saving = ref(false)
const error = ref('')
const document = ref<GuideDocument | null>(null)
const draft = ref<GuideContent>({})
const snapshot = ref('{}')
const language = ref<GuideLanguage>(locale.value.startsWith('zh') ? 'zh' : 'en')
const chapter = ref<GuideChapterId>('quickstart')
const os = ref<GuideOS>(/Win/i.test(navigator.platform) ? 'windows' : 'unix')
const mode = ref<'edit' | 'preview'>('edit')
const textarea = ref<HTMLTextAreaElement | null>(null)
const confirmation = ref<'restore' | 'reload' | null>(null)
const ready = computed(() => document.value !== null)
const dirty = computed(() => JSON.stringify(draft.value) !== snapshot.value)
const unpublished = computed(() => JSON.stringify(draft.value) !== JSON.stringify(document.value?.published ?? {}))
const defaults = computed(() => Object.fromEntries(guideChapters.map(item => [item.id, getDefaultGuideChapter(item.id, (key, params) => t(key, params ?? {}, { locale: language.value }), os.value)])) as Record<GuideChapterId, GuideChapterContent>)
const chapterFor = (id: GuideChapterId) => draft.value[language.value]?.[id] ?? defaults.value[id]
const current = computed(() => chapterFor(chapter.value))
const invalid = computed(() => Object.values(draft.value).some(chapters => Object.values(chapters).some(item => item && (!item.title.trim() || [...item.title].length > 120 || [...item.description].length > 400 || !item.markdown.trim() || new TextEncoder().encode(item.markdown).length > 100000))))
const variables = computed(() => {
  const endpoints = getGuideEndpoints(appStore.apiBaseUrl, window.location.origin)
  return { site_name: appStore.siteName, site_url: endpoints.site, api_root: endpoints.anthropic, openai_base_url: endpoints.openai, contact_info: appStore.contactInfo || t('beginnerGuide.errors.noContact'), doc_url: appStore.docUrl }
})

function updateChapter(patch: Partial<GuideChapterContent>) {
  draft.value = { ...draft.value, [language.value]: { ...draft.value[language.value], [chapter.value]: { ...current.value, ...patch } } }
}

function acceptDocument(value: GuideDocument) {
  document.value = value
  draft.value = JSON.parse(JSON.stringify(value.draft))
  snapshot.value = JSON.stringify(draft.value)
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [value] = await Promise.all([beginnerGuideAPI.admin(), loadLocaleMessages('zh'), loadLocaleMessages('en')])
    acceptDocument(value)
  }
  catch { error.value = t('guideManager.loadFailed') }
  finally { loading.value = false }
}

async function save(publish: boolean) {
  if (!document.value || saving.value || invalid.value) return
  saving.value = true
  error.value = ''
  try {
    acceptDocument(await beginnerGuideAPI.save(document.value.revision, draft.value, publish))
    appStore.showSuccess(t(publish ? 'guideManager.publishSuccess' : 'guideManager.saved'))
  } catch (cause) {
    error.value = t((cause as { status?: number; reason?: string }).status === 409 || (cause as { reason?: string }).reason === 'BEGINNER_GUIDE_CONFLICT' ? 'guideManager.conflict' : 'guideManager.saveFailed')
  } finally { saving.value = false }
}

function requestReload() {
  if (dirty.value) confirmation.value = 'reload'
  else void load()
}

function confirmAction() {
  if (confirmation.value === 'reload') void load()
  else if (confirmation.value === 'restore') {
    const chapters = { ...draft.value[language.value] }
    delete chapters[chapter.value]
    const content = { ...draft.value }
    if (Object.keys(chapters).length) content[language.value] = chapters
    else delete content[language.value]
    draft.value = content
  }
  confirmation.value = null
}

async function insertText(text: string, prefix = '', suffix = '', wrapSelection = false) {
  const input = textarea.value
  const start = input?.selectionStart ?? current.value.markdown.length
  const end = input?.selectionEnd ?? start
  const selection = wrapSelection ? current.value.markdown.slice(start, end) || text : text
  updateChapter({ markdown: current.value.markdown.slice(0, start) + prefix + selection + suffix + current.value.markdown.slice(end) })
  await nextTick()
  textarea.value?.focus()
  textarea.value?.setSelectionRange(start + prefix.length, start + prefix.length + selection.length)
}

function format(tool: 'bold' | 'italic' | 'heading') {
  const marker = tool === 'bold' ? '**' : tool === 'italic' ? '*' : '\n\n## '
  void insertText(t(tool === 'heading' ? 'guideManager.headingText' : 'guideManager.selection'), marker, tool === 'heading' ? '\n\n' : marker, true)
}

function insertBlock(tool: string) {
  const snippets: Record<string, string> = {
    bulletList: `- ${t('guideManager.itemText')}\n- ${t('guideManager.itemText')}`,
    numberedList: `1. ${t('guideManager.itemText')}\n2. ${t('guideManager.itemText')}`,
    quote: `> ${t('guideManager.quoteText')}`,
    code: `\`\`\`bash\n${t('guideManager.codeText')}\n\`\`\``,
    table: `| ${t('guideManager.tableHeader')} |\n| --- | --- |\n| ${t('guideManager.tableRow')} |`,
    image: `![${t('guideManager.imageText')}](https://example.com/image.png)`,
    link: `[${t('guideManager.linkText')}](https://example.com)`,
  }
  if (snippets[tool]) void insertText(snippets[tool]!, '\n\n', '\n\n')
}

function beforeUnload(event: BeforeUnloadEvent) { if (dirty.value) { event.preventDefault(); event.returnValue = '' } }
onBeforeRouteLeave(() => !dirty.value || window.confirm(t('guideManager.leaveWarning')))
onMounted(() => { void load(); window.addEventListener('beforeunload', beforeUnload) })
onUnmounted(() => window.removeEventListener('beforeunload', beforeUnload))
</script>

<style scoped>
.guide-manager { padding: 24px; }
.manager-toolbar { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 16px; padding-bottom: 20px; border-bottom: 1px solid #e5e7eb; }
.manager-status { display: grid; gap: 5px; font-size: 13px; }
.manager-status > span { color: #047857; font-weight: 600; }
.manager-status > span.changed { color: #b45309; }
.manager-status small { color: #6b7280; font-size: 11px; }
.manager-actions { display: flex; flex-wrap: wrap; gap: 8px; }
.manager-actions .btn { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; }
.manager-actions svg { flex-shrink: 0; }
.manager-error { margin: 14px 0; padding: 10px 12px; border-left: 3px solid #e11d48; background: #fff1f2; color: #9f1239; font-size: 13px; }
.manager-workspace { display: grid; grid-template-columns: 200px minmax(0, 1fr); gap: 24px; padding-top: 22px; }
.manager-chapters { min-width: 0; }
.manager-chapters nav { display: grid; gap: 4px; margin-top: 20px; }
.manager-chapters nav button { display: flex; min-height: 42px; align-items: center; gap: 8px; padding: 8px; border-left: 2px solid transparent; border-radius: 0 4px 4px 0; text-align: left; color: #6b7280; font-size: 12px; overflow-wrap: anywhere; }
.manager-chapters nav button.active { border-color: #2563eb; background: #eff6ff; color: #1d4ed8; }
.chapter-number { flex-shrink: 0; font-family: ui-monospace, monospace; font-size: 10px; opacity: .65; }
.custom-dot { width: 5px; height: 5px; flex-shrink: 0; margin-left: auto; border-radius: 50%; background: #059669; }
.manager-main { min-width: 0; }
.manager-fields { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 12px; }
.manager-content-bar { display: flex; align-items: center; gap: 12px; margin: 18px 0 10px; }
.manager-icon-button { display: inline-flex; width: 32px; height: 32px; flex-shrink: 0; align-items: center; justify-content: center; border-radius: 4px; font-size: 13px; }
.manager-icon-button:hover { background: #e5e7eb; }
.manager-icon-button:disabled { cursor: default; opacity: .35; }
.manager-editors { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); border-top: 1px solid #e5e7eb; }
.manager-editor { display: flex; min-width: 0; flex-direction: column; border-right: 1px solid #e5e7eb; }
.markdown-toolbar { display: flex; min-height: 46px; flex-wrap: wrap; align-items: center; gap: 4px; padding: 6px 6px 6px 0; border-bottom: 1px solid #e5e7eb; }
.manager-tool-select { max-width: 100%; border: 0; border-radius: 4px; background: transparent; font-size: 11px; }
.manager-variable-select { max-width: 170px; }
.manager-editor textarea { display: block; width: 100%; min-height: 620px; flex: 1; resize: vertical; border: 0; padding: 16px 16px 16px 0; background: transparent; font-family: ui-monospace, SFMono-Regular, Consolas, monospace; font-size: 12px; line-height: 1.9; overflow-wrap: anywhere; }
.manager-editor textarea:focus { outline: 0; box-shadow: inset 2px 0 #60a5fa; }
.manager-preview { --guide-line: #e5e7eb; --guide-muted: #6b7280; --guide-surface: #f8fafb; --guide-text: #1f2937; min-width: 0; padding: 20px 0 20px 24px; }
.preview-chapter-heading { margin-bottom: 24px; }
.preview-chapter-heading > span { color: #6b7280; font-size: 11px; }
.preview-chapter-heading h2 { margin-top: 6px; font-size: 19px; font-weight: 650; line-height: 1.6; overflow-wrap: anywhere; }
.preview-chapter-heading p { margin-top: 8px; color: #6b7280; font-size: 13px; overflow-wrap: anywhere; }
.manager-mode, .manager-mobile-selects { display: none; }
.dark .manager-toolbar, .dark .manager-editors, .dark .manager-editor, .dark .markdown-toolbar { border-color: #334155; }
.dark .manager-chapters nav button { color: #94a3b8; }
.dark .manager-chapters nav button.active { background: #172554; color: #93c5fd; }
.dark .manager-status > span { color: #6ee7b7; }
.dark .manager-status > span.changed { color: #fcd34d; }
.dark .manager-error { background: #4c1026; color: #fda4af; }
.dark .manager-icon-button:hover { background: #334155; }
.dark .manager-preview { --guide-line: #334155; --guide-muted: #94a3b8; --guide-surface: #192331; --guide-text: #e2e8f0; }
.guide-manager :is(button, select, a):focus-visible { outline: 2px solid #60a5fa; outline-offset: 3px; }
@media (max-width: 1399px) {
  .manager-editors { display: block; }
  .mode-edit .manager-preview, .mode-preview .manager-editor { display: none; }
  .manager-editor { border-right: 0; }
  .manager-preview { padding-left: 0; }
  .manager-mode { display: flex; padding: 2px; border: 1px solid #e5e7eb; border-radius: 5px; }
  .manager-mode button { min-width: 54px; padding: 3px 8px; border-radius: 3px; color: #6b7280; font-size: 12px; }
  .manager-mode button.active { background: #eff6ff; color: #1d4ed8; }
  .dark .manager-mode { border-color: #334155; }
  .dark .manager-mode button.active { background: #172554; color: #93c5fd; }
}
@media (max-width: 899px) {
  .guide-manager { padding: 18px; }
  .manager-workspace { display: block; }
  .manager-chapters { display: none; }
  .manager-mobile-selects { display: grid; grid-template-columns: 90px minmax(0, 1fr); gap: 10px; margin-bottom: 16px; }
  .manager-mobile-selects label { min-width: 0; font-size: 12px; }
  .manager-mobile-selects select { margin-top: 6px; }
  .mobile-template-os { grid-column: 1 / -1; }
  .manager-fields { grid-template-columns: minmax(0, 1fr); }
  .manager-actions .action-label { display: none; }
  .manager-status small { font-size: 10px; }
}
</style>
