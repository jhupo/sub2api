import { describe, expect, it } from 'vitest'
import { getGuideMarkdownBlocks, substituteGuideVariables, type GuideVariables } from '../guideMarkdown'
import { getDefaultGuideChapter } from '../guideDefaults'
import { guideChapters } from '../beginnerGuide'
import zh from '@/i18n/locales/zh/beginnerGuide'
import en from '@/i18n/locales/en/beginnerGuide'

const variables: GuideVariables = {
  site_name: 'Test site', site_url: 'https://console.example.com', api_root: 'https://api.example.com',
  openai_base_url: 'https://api.example.com/v1', contact_info: 'Support', doc_url: 'https://docs.example.com',
}

function html(markdown: string) {
  return getGuideMarkdownBlocks(markdown, variables).filter(block => block.kind === 'html').map(block => block.html).join('')
}

describe('guide Markdown', () => {
  it('keeps code copyable and substitutes current site values', () => {
    const markdown = '## {{site_name}}\n\n```bash\ncurl {{openai_base_url}}/models\n```\n\n[Keys](/keys)'
    const blocks = getGuideMarkdownBlocks(markdown, variables)
    expect(blocks[1]).toEqual({ kind: 'code', code: 'curl https://api.example.com/v1/models', language: 'bash' })
    expect(html(markdown)).toContain('Test site')
    expect(getGuideMarkdownBlocks(markdown, { ...variables, openai_base_url: 'https://new.example.com/v1' })[1]).toMatchObject({ code: 'curl https://new.example.com/v1/models' })
    expect(substituteGuideVariables('{{unknown}}', variables)).toBe('{{unknown}}')
  })

  it('removes unsafe HTML and URLs in published content and preview', () => {
    const output = html('<script>alert(1)</script><img src="javascript:alert(1)" onerror="alert(2)"><a href="javascript:alert(3)">Bad</a><p style="position:fixed" onclick="alert(4)">Safe</p><iframe src="https://example.com"></iframe>')
    expect(output).not.toMatch(/<script|onerror|onclick|javascript:|style=|<iframe|<img/i)
    expect(output).toContain('Safe')
  })

  it('opens external links safely, keeps internal links, and wraps wide tables', () => {
    const output = html('[External](https://example.com) [Internal](/keys)\n\n| Name | Value |\n| --- | --- |\n| A | B |\n\n- [x] Done\n\n![Test](/test.png)')
    const doc = new DOMParser().parseFromString(output, 'text/html')
    expect(doc.querySelector('a')?.rel).toBe('noopener noreferrer')
    expect(doc.querySelector('a[href="/keys"]')?.getAttribute('target')).toBeNull()
    expect(doc.querySelector('.guide-markdown-table table tbody td')?.textContent).toBe('A')
    expect(doc.querySelector('input')?.disabled).toBe(true)
    expect(doc.querySelector('img')?.getAttribute('loading')).toBe('lazy')
  })

  it('preserves reference links across extracted code blocks', () => {
    expect(html('[Docs][ref]\n\n```text\ncode\n```\n\n[ref]: https://example.com')).toContain('href="https://example.com/"')
  })
})

describe('editable built-in guide templates', () => {
  it.each(['zh', 'en'] as const)('provides all chapters in %s without missing translations', locale => {
    const messages = { beginnerGuide: locale === 'zh' ? zh : en }
    const translate = (key: string, params: Record<string, string | number> = {}) => {
      const value = key.split('.').reduce<unknown>((node, part) => node && typeof node === 'object' ? (node as Record<string, unknown>)[part] : undefined, messages)
      expect(typeof value, `Missing translation: ${locale}.${key}`).toBe('string')
      return (value as string).replace(/\{(\w+)\}/g, (match, name) => String(params[name] ?? match))
    }
    for (const chapter of guideChapters) {
      for (const os of ['unix', 'windows'] as const) {
        const content = getDefaultGuideChapter(chapter.id, translate, os)
        expect(content.title.length).toBeGreaterThan(1)
        expect(content.markdown.length).toBeGreaterThan(200)
        expect(new TextEncoder().encode(content.markdown).length).toBeLessThan(100000)
        expect(html(content.markdown)).not.toContain('beginnerGuide.')
      }
    }
  })
})
