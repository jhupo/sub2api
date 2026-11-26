export type GuideOS = 'unix' | 'windows'

export const guideChapters = [
  { id: 'quickstart', icon: 'bolt' },
  { id: 'codex', icon: 'terminal' },
  { id: 'vscode', icon: 'cube' },
  { id: 'claude', icon: 'sparkles' },
  { id: 'ccswitch', icon: 'swap' },
  { id: 'chat', icon: 'chatBubble' },
  { id: 'models', icon: 'grid' },
  { id: 'billing', icon: 'creditCard' },
  { id: 'troubleshooting', icon: 'questionCircle' },
] as const

export type GuideChapterId = typeof guideChapters[number]['id']

export const guideErrorIds = ['auth', 'balance', 'quota', 'permission', 'model', 'rate', 'upstream', 'network'] as const

export function getGuideEndpoints(configuredBase: string, siteOrigin: string) {
  let base: URL
  try {
    base = new URL(configuredBase.trim() || siteOrigin)
    if (!['http:', 'https:'].includes(base.protocol)) throw new Error('Invalid API protocol')
  } catch {
    base = new URL(siteOrigin)
  }
  base.username = ''
  base.password = ''
  base.search = ''
  base.hash = ''
  // Keep reverse-proxy prefixes while removing an already configured version suffix.
  base.pathname = base.pathname.replace(/\/+$/, '').replace(/\/v1$/, '')
  const root = base.toString().replace(/\/+$/, '')
  return { site: siteOrigin, anthropic: root, openai: `${root}/v1` }
}

function quoteShell(value: string, os: GuideOS): string {
  return os === 'windows' ? `'${value.replace(/'/g, "''")}'` : `'${value.replace(/'/g, "'\\''")}'`
}

export function getGuideExamples(endpoints: ReturnType<typeof getGuideEndpoints>, os: GuideOS) {
  const windows = os === 'windows'
  const shell = windows ? 'PowerShell' : 'bash'
  const env = (name: string, value: string) => windows
    ? `$env:${name} = ${quoteShell(value, os)}`
    : `export ${name}=${quoteShell(value, os)}`
  const curl = windows ? 'curl.exe' : 'curl'
  const modelBody = JSON.stringify({ model: 'MODEL_ID', input: 'Reply with hello.' })
  return {
    shell,
    nodeCheck: 'node --version\nnpm --version',
    codexInstall: 'npm install -g @openai/codex\ncodex --version',
    codexPath: windows ? '%USERPROFILE%\\.codex\\config.toml' : '~/.codex/config.toml',
    codexEnv: env('SUB2API_API_KEY', 'sk-REPLACE_WITH_YOUR_KEY'),
    codexConfig: [
      'model_provider = "sub2api"',
      'model = "MODEL_ID"',
      '',
      '[model_providers.sub2api]',
      'name = "Sub2API"',
      `base_url = ${JSON.stringify(endpoints.openai)}`,
      'wire_api = "responses"',
      'env_key = "SUB2API_API_KEY"',
    ].join('\n'),
    claudeEnv: [env('ANTHROPIC_BASE_URL', endpoints.anthropic), env('ANTHROPIC_AUTH_TOKEN', 'sk-REPLACE_WITH_YOUR_KEY')].join('\n'),
    claudeSettings: JSON.stringify({ env: { ANTHROPIC_BASE_URL: endpoints.anthropic, ANTHROPIC_AUTH_TOKEN: 'sk-REPLACE_WITH_YOUR_KEY' } }, null, 2),
    claudePath: windows ? '%USERPROFILE%\\.claude\\settings.json' : '~/.claude/settings.json',
    models: `${curl} ${quoteShell(`${endpoints.openai}/models`, os)} -H ${quoteShell('Authorization: Bearer sk-REPLACE_WITH_YOUR_KEY', os)}`,
    request: windows
      ? [
          `$body = ${quoteShell(modelBody, os)}`,
          `Invoke-RestMethod -Method Post -Uri ${quoteShell(`${endpoints.openai}/responses`, os)} -Headers @{ Authorization = 'Bearer sk-REPLACE_WITH_YOUR_KEY' } -ContentType 'application/json' -Body $body`,
        ].join('\n')
      : [
          `${curl} ${quoteShell(`${endpoints.openai}/responses`, os)} \\`,
          "  -H 'Authorization: Bearer sk-REPLACE_WITH_YOUR_KEY' \\",
          "  -H 'Content-Type: application/json' \\",
          `  -d ${quoteShell(modelBody, os)}`,
        ].join('\n'),
  }
}

export interface GuideProgress {
  steps: number[]
  chapters: GuideChapterId[]
}

export function readGuideProgress(storage: Pick<Storage, 'getItem'>, key: string): GuideProgress {
  try {
    const value: unknown = JSON.parse(storage.getItem(key) || '{}')
    const parsed = value && typeof value === 'object' ? value as Partial<GuideProgress> : {}
    return {
      steps: Array.isArray(parsed.steps) ? [...new Set(parsed.steps.filter(n => Number.isInteger(n) && n >= 1 && n <= 5))] : [],
      chapters: Array.isArray(parsed.chapters)
        ? [...new Set(parsed.chapters.filter(id => guideChapters.some(chapter => chapter.id === id)))] : [],
    }
  } catch {
    return { steps: [], chapters: [] }
  }
}
