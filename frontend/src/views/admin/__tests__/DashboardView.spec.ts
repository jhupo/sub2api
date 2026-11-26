import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

import type { DashboardStats } from '@/types'
import DashboardView from '../DashboardView.vue'

const { getSnapshotV2, getUsageTrend, getUserUsageTrend, getUserSpendingRanking } = vi.hoisted(() => ({
  getSnapshotV2: vi.fn(),
  getUsageTrend: vi.fn(),
  getUserUsageTrend: vi.fn(),
  getUserSpendingRanking: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    dashboard: {
      getSnapshotV2,
      getUsageTrend,
      getUserUsageTrend,
      getUserSpendingRanking
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn()
  })
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({
    push: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const formatLocalDate = (date: Date): string => {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

const createDashboardStats = (): DashboardStats => ({
  total_users: 0,
  today_new_users: 0,
  active_users: 0,
  hourly_active_users: 0,
  stats_updated_at: '',
  stats_stale: false,
  total_api_keys: 0,
  active_api_keys: 0,
  total_accounts: 0,
  normal_accounts: 0,
  error_accounts: 0,
  ratelimit_accounts: 0,
  overload_accounts: 0,
  total_requests: 0,
  total_input_tokens: 0,
  total_output_tokens: 0,
  total_cache_creation_tokens: 0,
  total_cache_read_tokens: 0,
  total_tokens: 0,
  total_cost: 0,
  total_actual_cost: 0,
  today_requests: 0,
  today_input_tokens: 0,
  today_output_tokens: 0,
  today_cache_creation_tokens: 0,
  today_cache_read_tokens: 0,
  today_tokens: 0,
  today_cost: 0,
  today_actual_cost: 0,
  average_duration_ms: 0,
  uptime: 0,
  rpm: 0,
  tpm: 0
})

const mountDashboard = () => mount(DashboardView, {
  global: {
    stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      LoadingSpinner: true,
      Icon: true,
      DateRangePicker: true,
      Select: true,
      ModelDistributionChart: true,
      TokenUsageTrend: true,
      Line: true
    }
  }
})

describe('admin DashboardView', () => {
  let wrapper: ReturnType<typeof mountDashboard> | undefined

  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date(2026, 10, 11, 12, 0, 0))
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    setActivePinia(createPinia())

    getSnapshotV2.mockReset()
    getUsageTrend.mockReset()
    getUserUsageTrend.mockReset()
    getUserSpendingRanking.mockReset()

    getSnapshotV2.mockResolvedValue({
      stats: createDashboardStats(),
      trend: [],
      models: []
    })
    getUsageTrend.mockResolvedValue({ trend: [] })
    getUserUsageTrend.mockResolvedValue({
      trend: [],
      start_date: '',
      end_date: '',
      granularity: 'hour'
    })
    getUserSpendingRanking.mockResolvedValue({
      ranking: [],
      total_actual_cost: 0,
      total_requests: 0,
      total_tokens: 0,
      start_date: '',
      end_date: ''
    })
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('uses last 24 hours as default dashboard range', async () => {
    wrapper = mountDashboard()

    await flushPromises()

    const now = new Date()
    const yesterday = new Date(now.getTime() - 24 * 60 * 60 * 1000)

    expect(getSnapshotV2).toHaveBeenCalledTimes(1)
    expect(getSnapshotV2).toHaveBeenCalledWith(expect.objectContaining({
      start_date: formatLocalDate(yesterday),
      end_date: formatLocalDate(now),
      granularity: 'hour'
    }))
  })

  it('refreshes only the lightweight trend each minute without rerunning ranking reports', async () => {
    wrapper = mountDashboard()
    await flushPromises()

    expect(getUsageTrend).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()

    expect(getUsageTrend).toHaveBeenCalledTimes(2)
    expect(getSnapshotV2).toHaveBeenCalledTimes(1)
    expect(getUserUsageTrend).toHaveBeenCalledTimes(1)
    expect(getUserSpendingRanking).toHaveBeenCalledTimes(1)
  })

  it('pauses in a hidden tab and catches up when visible again', async () => {
    wrapper = mountDashboard()
    await flushPromises()
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    await vi.advanceTimersByTimeAsync(120_000)
    expect(getUsageTrend).toHaveBeenCalledTimes(1)

    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    document.dispatchEvent(new Event('visibilitychange'))
    await flushPromises()
    expect(getUsageTrend).toHaveBeenCalledTimes(2)
  })

  it('allows automatic trend updates to be disabled', async () => {
    wrapper = mountDashboard()
    await flushPromises()
    await wrapper.get('[data-testid="dashboard-trend-auto-refresh"]').setValue(false)
    await vi.advanceTimersByTimeAsync(120_000)
    expect(getUsageTrend).toHaveBeenCalledTimes(1)
  })

  it('does not overlap slow trend requests or continue polling after unmount', async () => {
    wrapper = mountDashboard()
    await flushPromises()
    let resolveTrend!: (value: { trend: [] }) => void
    getUsageTrend.mockImplementationOnce(() => new Promise(resolve => { resolveTrend = resolve }))
    await vi.advanceTimersByTimeAsync(180_000)
    expect(getUsageTrend).toHaveBeenCalledTimes(2)
    resolveTrend({ trend: [] })
    await flushPromises()

    wrapper.unmount()
    wrapper = undefined
    await vi.advanceTimersByTimeAsync(120_000)
    document.dispatchEvent(new Event('visibilitychange'))
    expect(getUsageTrend).toHaveBeenCalledTimes(2)
  })

  it('does not poll a historical date range', async () => {
    wrapper = mountDashboard()
    await flushPromises()
    const picker = wrapper.findComponent({ name: 'DateRangePicker' })
    picker.vm.$emit('update:startDate', '2026-11-01')
    picker.vm.$emit('update:endDate', '2026-11-02')
    picker.vm.$emit('change', { startDate: '2026-11-01', endDate: '2026-11-02', preset: null })
    await flushPromises()
    expect(getUsageTrend).toHaveBeenCalledTimes(1)
    const previousCalls = getUsageTrend.mock.calls.length
    await vi.advanceTimersByTimeAsync(120_000)
    expect(getUsageTrend).toHaveBeenCalledTimes(previousCalls)
  })

  it('rolls a relative range over at midnight without repeatedly reloading heavy reports', async () => {
    vi.setSystemTime(new Date(2026, 10, 11, 23, 59, 50))
    wrapper = mountDashboard()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()
    expect(getSnapshotV2).toHaveBeenCalledTimes(2)
    expect(getSnapshotV2).toHaveBeenLastCalledWith(expect.objectContaining({
      start_date: '2026-11-11', end_date: '2026-11-12'
    }))
    await vi.advanceTimersByTimeAsync(120_000)
    expect(getSnapshotV2).toHaveBeenCalledTimes(2)
    expect(getUserUsageTrend).toHaveBeenCalledTimes(2)
    expect(getUserSpendingRanking).toHaveBeenCalledTimes(2)
  })

  it('keeps the previous trend visible when an automatic update fails', async () => {
    const initialTrend = [{ date: '2026-11-11 12:00', input_tokens: 100 }]
    getUsageTrend.mockResolvedValueOnce({ trend: initialTrend })
    wrapper = mountDashboard()
    await flushPromises()
    getUsageTrend.mockRejectedValueOnce(new Error('network timeout'))
    vi.spyOn(console, 'error').mockImplementation(() => {})

    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()
    const trend = wrapper.findComponent({ name: 'TokenUsageTrend' })
    expect(trend.props('trendData')).toEqual(initialTrend)
    expect(trend.props('refreshFailed')).toBe(true)
    expect(trend.props('checkedAt')).toBe(new Date(2026, 10, 11, 12).toISOString())
  })
})
