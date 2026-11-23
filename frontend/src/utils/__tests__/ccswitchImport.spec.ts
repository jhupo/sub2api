import { describe, expect, it } from 'vitest'
import { runInNewContext } from 'node:vm'
import {
  GROK_CC_SWITCH_MODEL,
  OPENAI_CC_SWITCH_CODEX_MODEL,
  SUB2API_CC_SWITCH_USAGE_SCRIPT,
  buildCcSwitchImportDeeplink
} from '@/utils/ccswitchImport'
import type { GroupPlatform } from '@/types'

function paramsFromDeeplink(deeplink: string): URLSearchParams {
  const query = deeplink.split('?')[1] || ''
  return new URLSearchParams(query)
}

function decodeBase64Utf8(value: string): string {
  return new TextDecoder().decode(Uint8Array.from(atob(value), (character) => character.charCodeAt(0)))
}

describe('ccswitchImport utils', () => {
  it('defaults OpenAI CC Switch imports to the current Codex model', () => {
    expect(OPENAI_CC_SWITCH_CODEX_MODEL).toBe('gpt-6.2-sol')
  })

  it('defaults Grok Build imports to the current Grok model', () => {
    expect(GROK_CC_SWITCH_MODEL).toBe('grok-4.5')
  })

  const baseInput = {
    baseUrl: 'https://api.example.com',
    providerName: 'Sub2API',
    apiKey: 'sk-test',
    usageScript: 'return true'
  }

  it('adds the Codex model parameter for OpenAI imports', () => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        ...baseInput,
        platform: 'openai',
        clientType: 'claude'
      })
    )

    expect(params.get('resource')).toBe('provider')
    expect(params.get('app')).toBe('codex')
    expect(params.get('endpoint')).toBe(baseInput.baseUrl)
    expect(params.get('model')).toBe(OPENAI_CC_SWITCH_CODEX_MODEL)
    expect(atob(params.get('usageScript') || '')).toBe(baseInput.usageScript)
    expect(params.get('apiFormat')).toBe('openai_responses')
    expect(params.get('configFormat')).toBe('json')

    const config = JSON.parse(decodeBase64Utf8(params.get('config') || ''))
    expect(config.auth).toEqual({ OPENAI_API_KEY: baseInput.apiKey })
    expect(config.config).toContain('model_provider = "sub2api"')
    expect(config.config).toContain('model = "gpt-6.2-sol"')
    expect(config.config).toContain('review_model = "gpt-6.2-sol"')
    expect(config.config).toContain('model_reasoning_effort = "high"')
    expect(config.config).toContain(`base_url = "${baseInput.baseUrl}"`)
    expect(config.config).toContain('wire_api = "responses"')
    expect(config.config).toContain('requires_openai_auth = false')
    expect(config.config).toContain('experimental_bearer_token = "sk-test"')
    expect(config.config).toContain('http_headers = { "x-openai-actor-authorization" = "local-image-extension" }')
    expect(config.config).not.toContain('[features]')
  })

  it('preserves Unicode and escaped provider names in the embedded config', () => {
    const providerName = '\u6d4b\u8bd5 \u7f51\u5173 "A" \\ gateway'
    const usageScript = 'return "\u4f59\u989d"'
    const apiKey = 'sk-quote"slash\\line\n'
    const params = paramsFromDeeplink(buildCcSwitchImportDeeplink({
      ...baseInput, platform: 'openai', clientType: 'claude', providerName, usageScript, apiKey
    }))

    expect(params.get('name')).toBe(providerName)
    expect(decodeBase64Utf8(params.get('usageScript') || '')).toBe(usageScript)
    const config = JSON.parse(decodeBase64Utf8(params.get('config') || ''))
    expect(config.config).toContain(`name = ${JSON.stringify(providerName)}`)
    expect(config.auth.OPENAI_API_KEY).toBe(apiKey)
    expect(config.config).toContain(`experimental_bearer_token = ${JSON.stringify(apiKey)}`)
  })

  it.each([
    ['https://api.example.com', 'https://api.example.com/v1/usage'],
    ['https://api.example.com/', 'https://api.example.com/v1/usage'],
    ['https://api.example.com/v1', 'https://api.example.com/v1/usage'],
    ['https://api.example.com/v1/', 'https://api.example.com/v1/usage'],
    ['https://api.example.com/gateway/v1/', 'https://api.example.com/gateway/v1/usage'],
    ['https://api.example.com/antigravity', 'https://api.example.com/antigravity/v1/usage']
  ])('queries usage once under /v1 for endpoint %s', (endpoint, expectedUrl) => {
    const usage = runInNewContext(SUB2API_CC_SWITCH_USAGE_SCRIPT
      .replaceAll('{{baseUrl}}', endpoint)
      .replaceAll('{{apiKey}}', 'sk-test'))
    expect(usage.request.url).toBe(expectedUrl)
    expect(usage.request.headers.Authorization).toBe('Bearer sk-test')
    expect(usage.extractor({ remaining: 0, unit: 'USD', is_active: false }))
      .toEqual({ isValid: false, remaining: 0, unit: 'USD' })
    expect(usage.extractor({ quota: { remaining: 9, unit: 'CNY' } }))
      .toEqual({ isValid: true, remaining: 9, unit: 'CNY' })
    expect(usage.extractor({ balance: 7 }).remaining).toBe(7)
  })

  it.each(['openai', 'grok'] as const)('exports a working usage query for %s endpoints', (platform) => {
    const params = paramsFromDeeplink(buildCcSwitchImportDeeplink({
      ...baseInput, baseUrl: 'https://api.example.com/v1/', platform, clientType: 'claude',
      usageScript: SUB2API_CC_SWITCH_USAGE_SCRIPT
    }))
    expect(params.get('endpoint')).toBe('https://api.example.com/v1')
    const usage = runInNewContext(decodeBase64Utf8(params.get('usageScript') || '')
      .replaceAll('{{baseUrl}}', params.get('endpoint')!)
      .replaceAll('{{apiKey}}', params.get('apiKey')!))
    expect(usage.request.url).toBe('https://api.example.com/v1/usage')
    if (platform === 'openai') {
      const config = JSON.parse(decodeBase64Utf8(params.get('config') || ''))
      expect(config.config).toContain('base_url = "https://api.example.com/v1"')
    }
  })

  it('encodes long usage scripts without exceeding JavaScript argument limits', () => {
    const usageScript = 'x'.repeat(150_000)
    const params = paramsFromDeeplink(buildCcSwitchImportDeeplink({
      ...baseInput, platform: 'openai', clientType: 'claude', usageScript
    }))
    expect(decodeBase64Utf8(params.get('usageScript') || '')).toBe(usageScript)
  })

  it.each([
    'https://api.example.com',
    'https://api.example.com/',
    'https://api.example.com/v1',
    'https://api.example.com/v1/'
  ])('imports Grok Build with one /v1 suffix for base URL %s', (baseUrl) => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        ...baseInput,
        baseUrl,
        platform: 'grok',
        clientType: 'claude'
      })
    )

    expect(params.get('app')).toBe('grokbuild')
    expect(params.get('endpoint')).toBe('https://api.example.com/v1')
    expect(params.get('model')).toBe(GROK_CC_SWITCH_MODEL)
  })

  it.each([
    { platform: 'anthropic' as GroupPlatform, clientType: 'claude' as const, app: 'claude' },
    { platform: 'gemini' as GroupPlatform, clientType: 'gemini' as const, app: 'gemini' }
  ])('does not add a model parameter for $platform imports', ({ platform, clientType, app }) => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        ...baseInput,
        platform,
        clientType
      })
    )

    expect(params.get('app')).toBe(app)
    expect(params.get('endpoint')).toBe(baseInput.baseUrl)
    expect(params.has('model')).toBe(false)
    expect(params.has('config')).toBe(false)
    expect(params.has('apiFormat')).toBe(false)
  })

  it('keeps Antigravity imports on the selected client endpoint without a model parameter', () => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        ...baseInput,
        platform: 'antigravity',
        clientType: 'gemini'
      })
    )

    expect(params.get('app')).toBe('gemini')
    expect(params.get('endpoint')).toBe(`${baseInput.baseUrl}/antigravity`)
    expect(params.has('model')).toBe(false)
  })
})
