import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import AntigravityQuotaWindows from '../AntigravityQuotaWindows.vue'
import type { AccountUsageInfo } from '@/types'

describe('AntigravityQuotaWindows', () => {
  it('keeps each upstream window and skips unknown quota', () => {
    const wrapper = mount(AntigravityQuotaWindows, {
      props: { usage: {
        antigravity_quota_groups: [{ displayName: 'Gemini', buckets: [
          { bucketId: 'short', displayName: '5 hours', window: '5h', remainingFraction: 0.8, resetTime: '' },
          { bucketId: 'week', displayName: 'Weekly', window: '7d', remainingFraction: 0, resetTime: '' },
          { bucketId: 'unknown', displayName: 'Unknown', window: '', remainingFraction: null, resetTime: '' }
        ] }]
      } as AccountUsageInfo },
      global: { stubs: { UsageProgressBar: { props: ['label', 'utilization'], template: '<div>{{ label }}:{{ utilization }}</div>' } } }
    })
    expect(wrapper.text()).toContain('Gemini')
    expect(wrapper.text()).toContain('5 hours')
    expect(wrapper.text()).toContain('Weekly')
    expect(wrapper.text()).toContain(':100')
    expect(wrapper.text()).not.toContain('Unknown')
  })
})
