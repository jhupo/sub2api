import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import CacheReadAdjustmentStats from '../CacheReadAdjustmentStats.vue'

const api = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/admin/usage', () => ({ getCacheReadAdjustmentDaily: api.get }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('CacheReadAdjustmentStats', () => {
  beforeEach(() => {
    api.get.mockReset()
    api.get.mockResolvedValue([{ date: '2026-10-30', account_id: 29, requests: 3, priced_requests: 2,
      shifted_tokens: 800, additional_cost: 0.03, balance_additional_cost: 0.01, subscription_additional_cost: 0.02 }])
  })

  it('separates wallet revenue from subscription quota and filters by account', async () => {
    const wrapper = mount(CacheReadAdjustmentStats)
    await flushPromises()
    expect(wrapper.text()).toContain('$0.010000')
    expect(wrapper.text()).toContain('$0.020000')
    expect(wrapper.text()).toContain('2 / 3')
    await wrapper.get('input[type="number"]').setValue('29')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.get).toHaveBeenLastCalledWith(expect.objectContaining({ account_id: 29, timezone: expect.any(String) }))
    wrapper.unmount()
  })

  it('shows errors and permits retry', async () => {
    api.get.mockRejectedValueOnce(new Error('network failed'))
    const wrapper = mount(CacheReadAdjustmentStats)
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('#29')
    wrapper.unmount()
  })
})
