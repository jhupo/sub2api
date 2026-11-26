import { Marked, type Token, type TokensList } from 'marked'
import DOMPurify from 'dompurify'
import { sanitizeUrl } from './url'

export const guidePlaceholders = ['site_name', 'site_url', 'api_root', 'openai_base_url', 'contact_info', 'doc_url'] as const
export type GuideVariables = Record<typeof guidePlaceholders[number], string>

const parser = new Marked({ gfm: true, breaks: false, async: false })

export function substituteGuideVariables(markdown: string, variables: GuideVariables): string {
  return markdown.replace(/\{\{(site_name|site_url|api_root|openai_base_url|contact_info|doc_url)\}\}/g, (_, key: keyof GuideVariables) => variables[key])
}

function sanitizeMarkdown(html: string): string {
  const safe = DOMPurify.sanitize(html, {
    ALLOWED_TAGS: ['h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'p', 'br', 'a', 'img', 'pre', 'code', 'em', 'strong', 'del', 'blockquote', 'ul', 'ol', 'li', 'hr', 'table', 'thead', 'tbody', 'tr', 'th', 'td', 'input', 'details', 'summary'],
    ALLOWED_ATTR: ['href', 'src', 'alt', 'title', 'start', 'type', 'checked', 'disabled', 'align'],
  })
  const doc = new DOMParser().parseFromString(safe, 'text/html')
  for (const link of doc.querySelectorAll('a')) {
    const href = link.getAttribute('href') || ''
    if (href.startsWith('#')) continue
    const valid = sanitizeUrl(href, { allowRelative: true })
    if (!valid) link.removeAttribute('href')
    else {
      link.setAttribute('href', valid)
      if (/^https?:/i.test(valid)) {
        link.setAttribute('target', '_blank')
        link.setAttribute('rel', 'noopener noreferrer')
      }
    }
  }
  for (const image of doc.querySelectorAll('img')) {
    const valid = sanitizeUrl(image.getAttribute('src') || '', { allowRelative: true })
    if (!valid) image.remove()
    else {
      image.setAttribute('src', valid)
      image.setAttribute('loading', 'lazy')
    }
  }
  for (const input of doc.querySelectorAll('input')) {
    if (input.type !== 'checkbox') input.remove()
    else input.disabled = true
  }
  for (const table of doc.querySelectorAll('table')) {
    const wrapper = doc.createElement('div')
    wrapper.className = 'guide-markdown-table'
    wrapper.tabIndex = 0
    table.replaceWith(wrapper)
    wrapper.append(table)
  }
  return doc.body.innerHTML
}

export function getGuideMarkdownBlocks(markdown: string, variables: GuideVariables) {
  const tokens = parser.lexer(substituteGuideVariables(markdown, variables))
  const blocks: ({ kind: 'html'; html: string } | { kind: 'code'; code: string; language: string })[] = []
  let group: Token[] = []
  const flush = () => {
    if (!group.length) return
    const list = Object.assign(group, { links: tokens.links }) as TokensList
    blocks.push({ kind: 'html', html: sanitizeMarkdown(parser.parser(list)) })
    group = []
  }
  for (const token of tokens) {
    if (token.type === 'code') {
      flush()
      blocks.push({ kind: 'code', code: token.text, language: token.lang || 'text' })
    } else group.push(token)
  }
  flush()
  return blocks
}
