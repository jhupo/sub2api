import { describe, expect, it, vi } from 'vitest'
import { useGeminiOAuth } from '../useGeminiOAuth'

const { showError, getCapabilities } = vi.hoisted(() => ({ showError: vi.fn(), getCapabilities: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError }) }))
vi.mock('@/api/admin', () => ({ adminAPI: { gemini: { getCapabilities } } }))

describe('Gemini OAuth credentials', () => {
  it('reports unavailable authorization settings without enabling an unverified mode', async () => {
    getCapabilities.mockRejectedValueOnce(new Error('unavailable'))
    expect(await useGeminiOAuth().getCapabilities()).toBeNull()
    expect(showError).toHaveBeenCalledWith('admin.accounts.oauth.gemini.failedToLoadCapabilities')
  })
  it('includes the authorized email when the create and edit forms build credentials', () => {
    const { buildCredentials } = useGeminiOAuth()
    expect(buildCredentials({
      email: 'account@example.com',
      access_token: 'access',
      refresh_token: 'refresh',
      expires_at: 1900000000,
      oauth_type: 'antigravity',
      project_id: 'project',
      tier_id: 'google_ai_pro'
    })).toEqual(expect.objectContaining({
      email: 'account@example.com',
      access_token: 'access',
      refresh_token: 'refresh',
      expires_at: '1900000000',
      oauth_type: 'antigravity',
      project_id: 'project',
      tier_id: 'google_ai_pro'
    }))
  })
})
