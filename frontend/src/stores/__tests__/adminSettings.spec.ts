import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useAdminSettingsStore } from '@/stores/adminSettings'

const mocks = vi.hoisted(() => ({
  getSettings: vi.fn(),
  getPaymentConfig: vi.fn(),
  getStateSettings: vi.fn()
}))

vi.mock('@/api', () => ({
  adminAPI: {
    settings: { getSettings: mocks.getSettings },
    payment: { getConfig: mocks.getPaymentConfig }
  }
}))

vi.mock('@/api/admin/upstreamState', () => ({
  upstreamStateApi: { settings: mocks.getStateSettings }
}))

describe('useAdminSettingsStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
    vi.clearAllMocks()
    mocks.getSettings.mockResolvedValue({})
    mocks.getPaymentConfig.mockResolvedValue({ data: { enabled: false } })
    mocks.getStateSettings.mockResolvedValue({ enabled: false })
  })

  it('applies state navigation settings even when an unrelated request fails', async () => {
    mocks.getPaymentConfig.mockRejectedValue(new Error('payment unavailable'))
    mocks.getStateSettings.mockResolvedValue({ enabled: true })

    const store = useAdminSettingsStore()
    await store.fetch()

    expect(store.upstreamStateEnabled).toBe(true)
    expect(localStorage.getItem('upstream_state_enabled_cached')).toBe('true')
    expect(store.loaded).toBe(true)
  })

  it('keeps the cached state setting when its own request fails', async () => {
    localStorage.setItem('upstream_state_enabled_cached', 'true')
    mocks.getStateSettings.mockRejectedValue(new Error('state unavailable'))

    const store = useAdminSettingsStore()
    await store.fetch()

    expect(store.upstreamStateEnabled).toBe(true)
    expect(localStorage.getItem('upstream_state_enabled_cached')).toBe('true')
  })
})
