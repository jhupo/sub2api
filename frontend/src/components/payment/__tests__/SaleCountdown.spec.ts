import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, describe, expect, it, vi } from 'vitest'
import SaleCountdown from '../SaleCountdown.vue'

describe('SaleCountdown', () => {
  afterEach(() => vi.useRealTimers())
  it('counts down to removal and emits expiry once', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-09T12:00:00Z'))
    const wrapper = mount(SaleCountdown, {
      props: { endsAt: '2026-11-09T12:00:02Z' },
      global: { plugins: [createI18n({ legacy: false, locale: 'en', messages: { en: {} }, missingWarn: false, fallbackWarn: false })] },
    })
    expect(wrapper.text()).toContain('00:00:02')
    await vi.advanceTimersByTimeAsync(2000)
    expect(wrapper.text()).toContain('quotaFeatures.offSale')
    expect(wrapper.emitted('expired')).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(5000)
    expect(wrapper.emitted('expired')).toHaveLength(1)
    wrapper.unmount()
  })
})
