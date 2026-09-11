import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { AccountUsageInfo } from '@/types'
import GeminiQuotaWindows from '../GeminiQuotaWindows.vue'
import UsageProgressBar from '../UsageProgressBar.vue'

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key })
}))

function render(data: Partial<AccountUsageInfo>) {
  return mount(GeminiQuotaWindows, { props: { usage: data as AccountUsageInfo } })
}
const bucket = (remainingFraction: number | null, window = 'weekly') => ({
  bucketId: window, displayName: window, window, remainingFraction, resetTime: '2026-10-07T00:00:00Z'
})

describe('Gemini compact quota', () => {
  it('selects the limiting window per upstream group and displays remaining, not used', () => {
    const wrapper = render({ antigravity_quota_groups: [
      { displayName: 'Gemini Models', buckets: [bucket(0.8), bucket(0.3, 'five_hour')] },
      { displayName: 'Claude and GPT models', buckets: [bucket(0.5)] }
    ] })
    const bars = wrapper.findAllComponents(UsageProgressBar)
    expect(bars.map(bar => bar.props('utilization'))).toEqual([30, 50])
    expect(bars.every(bar => bar.props('remainingCapacity'))).toBe(true)
    expect(wrapper.get('summary').text()).toContain('5h')
    expect(wrapper.get('summary').text()).toContain('Claude / GPT')
    expect(wrapper.get('summary').attributes('title')).toContain('80%')
    expect(wrapper.get('details').attributes('open')).toBeUndefined()
    wrapper.unmount()
  })

  it('preserves unknown quota instead of rendering zero; real zero remains zero', () => {
    const wrapper = render({ antigravity_quota_groups: [
      { displayName: 'Unknown', buckets: [bucket(null), bucket(Number.NaN), bucket(2)] },
      { displayName: 'Exhausted', buckets: [bucket(0)] }
    ] })
    expect(wrapper.get('summary').text()).toContain('—')
    expect(wrapper.findAllComponents(UsageProgressBar)).toHaveLength(1)
    expect(wrapper.getComponent(UsageProgressBar).props('utilization')).toBe(0)
    wrapper.unmount()
  })

  it('does not merge independent model quotas and keeps overflow in details', () => {
    const wrapper = render({ antigravity_quota: {
      'gemini-a': { utilization: 20, reset_time: null },
      'gemini-b': { utilization: 80, reset_time: null },
      'gemini-c': { utilization: 100, reset_time: null }
    } })
    expect(wrapper.findAll('[data-testid="quota-summary-row"]')).toHaveLength(2)
    expect(wrapper.get('summary').text()).toContain('+1')
    expect(wrapper.get('summary').text()).not.toContain('gemini-c')
    expect(wrapper.text()).toContain('gemini-c')
    wrapper.unmount()
  })
})
