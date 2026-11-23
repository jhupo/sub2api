import type { GroupPlatform } from '@/types'
import { DEFAULT_CODEX_MODEL, DEFAULT_CODEX_REASONING_EFFORT } from '@/utils/codexDefaults'
import { buildCodexApiKeyAuthConfig } from '@/utils/codexAuthConfig'

export const OPENAI_CC_SWITCH_CODEX_MODEL = DEFAULT_CODEX_MODEL
export const GROK_CC_SWITCH_MODEL = 'grok-4.5'

export const SUB2API_CC_SWITCH_USAGE_SCRIPT = String.raw`({
  request: {
    url: "{{baseUrl}}".replace(/\/+$/, "").replace(/\/v1$/i, "") + "/v1/usage",
    method: "GET",
    headers: { "Authorization": "Bearer {{apiKey}}" }
  },
  extractor: function(response) {
    const remaining = response?.remaining ?? response?.quota?.remaining ?? response?.balance;
    const unit = response?.unit ?? response?.quota?.unit ?? "USD";
    return {
      isValid: response?.is_active ?? response?.isValid ?? true,
      remaining,
      unit
    };
  }
})`

export type CcSwitchClientType = 'claude' | 'gemini'

export interface CcSwitchImportConfig {
  app: string
  endpoint: string
  model?: string
}

export interface CcSwitchImportDeeplinkInput {
  baseUrl: string
  platform?: GroupPlatform | null
  clientType: CcSwitchClientType
  providerName: string
  apiKey: string
  usageScript: string
}

function withV1Endpoint(baseUrl: string): string {
  const normalizedBaseUrl = baseUrl.replace(/\/+$/, '')
  return normalizedBaseUrl.endsWith('/v1') ? normalizedBaseUrl : `${normalizedBaseUrl}/v1`
}

function encodeBase64Utf8(value: string): string {
  return btoa(Array.from(new TextEncoder().encode(value), (byte) => String.fromCharCode(byte)).join(''))
}

function buildCodexImportConfig(input: CcSwitchImportDeeplinkInput, config: CcSwitchImportConfig): string {
  const content = `model_provider = "sub2api"
model = "${DEFAULT_CODEX_MODEL}"
review_model = "${DEFAULT_CODEX_MODEL}"
model_reasoning_effort = "${DEFAULT_CODEX_REASONING_EFFORT}"

[model_providers.sub2api]
name = ${JSON.stringify(input.providerName)}
base_url = ${JSON.stringify(config.endpoint)}
wire_api = "responses"
${buildCodexApiKeyAuthConfig(input.apiKey)}`

  return JSON.stringify({ auth: { OPENAI_API_KEY: input.apiKey }, config: content })
}

export function resolveCcSwitchImportConfig(
  platform: GroupPlatform | undefined | null,
  clientType: CcSwitchClientType,
  baseUrl: string
): CcSwitchImportConfig {
  const normalizedBaseUrl = baseUrl.replace(/\/+$/, '')
  switch (platform || 'anthropic') {
    case 'antigravity':
      return {
        app: clientType === 'gemini' ? 'gemini' : 'claude',
        endpoint: `${normalizedBaseUrl}/antigravity`
      }
    case 'openai':
      return {
        app: 'codex',
        endpoint: normalizedBaseUrl,
        model: OPENAI_CC_SWITCH_CODEX_MODEL
      }
    case 'gemini':
      return {
        app: 'gemini',
        endpoint: normalizedBaseUrl
      }
    case 'grok':
      return {
        app: 'grokbuild',
        endpoint: withV1Endpoint(normalizedBaseUrl),
        model: GROK_CC_SWITCH_MODEL
      }
    default:
      return {
        app: 'claude',
        endpoint: normalizedBaseUrl
      }
  }
}

export function buildCcSwitchImportDeeplink(input: CcSwitchImportDeeplinkInput): string {
  const config = resolveCcSwitchImportConfig(input.platform, input.clientType, input.baseUrl)
  const entries: [string, string][] = [
    ['resource', 'provider'],
    ['app', config.app],
    ['name', input.providerName],
    ['homepage', input.baseUrl],
    ['endpoint', config.endpoint],
    ['apiKey', input.apiKey],
    ['configFormat', 'json'],
    ['usageEnabled', 'true'],
    ['usageScript', encodeBase64Utf8(input.usageScript)],
    ['usageAutoInterval', '30']
  ]

  if (config.model) {
    entries.splice(2, 0, ['model', config.model])
  }

  if (config.app === 'codex') {
    entries.push(['apiFormat', 'openai_responses'])
    entries.push(['config', encodeBase64Utf8(buildCodexImportConfig(input, config))])
  }

  return `ccswitch://v1/import?${new URLSearchParams(entries).toString()}`
}
