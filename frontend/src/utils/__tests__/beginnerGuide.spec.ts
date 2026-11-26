import { describe, expect, it } from 'vitest'
import { getGuideEndpoints, getGuideExamples, readGuideProgress } from '../beginnerGuide'

describe('beginner guide endpoints and examples', () => {
  it.each(['https://api.example.com', 'https://api.example.com/', 'https://api.example.com/v1/', 'https://api.example.com/v1'])('normalizes %s without duplicating v1', configured => {
    expect(getGuideEndpoints(configured, 'https://console.example.com')).toEqual({
      site: 'https://console.example.com',
      openai: 'https://api.example.com/v1',
      anthropic: 'https://api.example.com',
    })
  })

  it('preserves reverse-proxy prefixes and removes credentials, query and fragment', () => {
    expect(getGuideEndpoints('https://name:secret@api.example.com/proxy/api/v1/?token=secret#test', 'https://console.example.com').anthropic).toBe('https://api.example.com/proxy/api')
  })

  it.each(['', 'javascript:alert(1)', 'not an endpoint'])('falls back to the local site for an invalid address: %s', configured => {
    expect(getGuideEndpoints(configured, 'https://console.example.com').openai).toBe('https://console.example.com/v1')
  })

  it('uses root for Claude and a single version suffix for Responses', () => {
    const examples = getGuideExamples(getGuideEndpoints('https://api.example.com/proxy/v1', 'https://console.example.com'), 'unix')
    expect(examples.claudeEnv).toContain("ANTHROPIC_BASE_URL='https://api.example.com/proxy'")
    expect(examples.request).toContain('https://api.example.com/proxy/v1/responses')
    expect(examples.request).not.toContain('\n+')
    expect(JSON.parse(examples.claudeSettings).env.ANTHROPIC_BASE_URL).toBe('https://api.example.com/proxy')
  })

  it('switches Windows examples to PowerShell, including JSON request handling', () => {
    const examples = getGuideExamples(getGuideEndpoints('', 'http://localhost:18080'), 'windows')
    expect(examples.codexPath).toBe('%USERPROFILE%\\.codex\\config.toml')
    expect(examples.codexEnv).toContain('$env:SUB2API_API_KEY')
    expect(examples.request).toContain('Invoke-RestMethod')
    expect(examples.request).not.toContain('export ')
    expect(examples.models).toMatch(/^curl\.exe /)
  })
})

describe('beginner guide saved progress', () => {
  it('discards malformed and out-of-range entries and duplicates', () => {
    const storage = { getItem: () => JSON.stringify({ steps: [1, 2, 2, -1, 8, '3'], chapters: ['codex', 'codex', 'missing', {}] }) }
    expect(readGuideProgress(storage, 'user-1')).toEqual({ steps: [1, 2], chapters: ['codex'] })
  })

  it.each(['invalid JSON', 'null', '[]'])('recovers safely from %s', data => {
    expect(readGuideProgress({ getItem: () => data }, 'user-1')).toEqual({ steps: [], chapters: [] })
  })

  it('remains readable when browser storage is blocked', () => {
    expect(readGuideProgress({ getItem: () => { throw new Error('blocked') } }, 'user-1')).toEqual({ steps: [], chapters: [] })
  })
})
