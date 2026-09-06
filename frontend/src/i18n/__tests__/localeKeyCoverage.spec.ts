import { readdirSync, readFileSync } from 'node:fs'
import { extname, join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

const sourceRoot = resolve(process.cwd(), 'src')
const sourceExtensions = new Set(['.ts', '.tsx', '.vue'])
const staticTranslationCall = /(?:\$t|\bt|i18n\.global\.t)\(\s*(['"`])([^'"`]+)\1/g

function collectMessageKeys(node: unknown, path = '', out = new Set<string>()): Set<string> {
  if (path) out.add(path)
  if (typeof node === 'string' || typeof node === 'number' || typeof node === 'boolean') {
    return out
  }
  if (Array.isArray(node)) {
    node.forEach((item, index) => collectMessageKeys(item, `${path}.${index}`, out))
    return out
  }
  if (node && typeof node === 'object') {
    for (const [key, value] of Object.entries(node as Record<string, unknown>)) {
      collectMessageKeys(value, path ? `${path}.${key}` : key, out)
    }
  }
  return out
}

function sourceFiles(directory: string): string[] {
  const files: string[] = []
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name)
    if (entry.isDirectory()) {
      if (entry.name !== '__tests__' && entry.name !== 'locales') files.push(...sourceFiles(path))
      continue
    }
    if (sourceExtensions.has(extname(entry.name)) && !/\.(?:spec|test)\.[^.]+$/.test(entry.name)) {
      files.push(path)
    }
  }
  return files
}

function collectStaticTranslationKeys(): Set<string> {
  const keys = new Set<string>()
  for (const file of sourceFiles(sourceRoot)) {
    const source = readFileSync(file, 'utf8')
    for (const match of source.matchAll(staticTranslationCall)) {
      const interpolatedAt = match[2].indexOf('${')
      const staticPath = interpolatedAt >= 0 ? match[2].slice(0, interpolatedAt) : match[2]
      if (interpolatedAt >= 0 && !staticPath.endsWith('.')) continue
      const key = staticPath.replace(/\.$/, '')
      if (key) keys.add(key)
    }
  }
  return keys
}

function difference(left: Set<string>, right: Set<string>): string[] {
  return [...left].filter((key) => !right.has(key)).sort()
}

describe('locale key coverage', () => {
  const enKeys = collectMessageKeys(en)
  const zhKeys = collectMessageKeys(zh)

  it('keeps English and Chinese locale keys in sync', () => {
    expect({ onlyInEnglish: difference(enKeys, zhKeys), onlyInChinese: difference(zhKeys, enKeys) }).toEqual({
      onlyInEnglish: [],
      onlyInChinese: [],
    })
  })

  it('defines every statically referenced translation key', () => {
    const referencedKeys = collectStaticTranslationKeys()
    expect({
      missingInEnglish: difference(referencedKeys, enKeys),
      missingInChinese: difference(referencedKeys, zhKeys),
    }).toEqual({ missingInEnglish: [], missingInChinese: [] })
  })
})
