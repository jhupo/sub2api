import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import CacheReadAdjustmentModal from '../CacheReadAdjustmentModal.vue'

const api = vi.hoisted(() => ({ get: vi.fn(), update: vi.fn(), list: vi.fn() }))
const showSuccess = vi.hoisted(() => vi.fn())
vi.mock('@/api/admin', () => ({ adminAPI: {
  settings: { getCacheReadAdjustmentSettings: api.get, updateCacheReadAdjustmentSettings: api.update },
  accounts: { list: api.list }
} }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const mountModal = () => mount(CacheReadAdjustmentModal, {
  props: { show: true, selectedIds: [12] },
  global: { stubs: { BaseDialog: { template: '<section><slot /><slot name="footer" /></section>' } } }
})

describe('CacheReadAdjustmentModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.get.mockResolvedValue({ enabled: false, percent: 10, account_ids: [8, 90] })
    api.list.mockResolvedValue({ items: [{ id: 7, name: 'Account A', platform: 'openai' }, { id: 8, name: 'Account B', platform: 'anthropic' }], total: 30 })
    api.update.mockResolvedValue({})
  })

  it('saves the switch, percentage, and selected accounts while preserving other pages', async () => {
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get('#cache-adjustment-enabled').trigger('click')
    await wrapper.get('#cache-adjustment-percent').setValue('25')
    await wrapper.get('input[type="checkbox"][value="7"]').setValue(true)
    await wrapper.get('#cache-read-adjustment-form').trigger('submit')
    await flushPromises()
    expect(api.update).toHaveBeenCalledWith({ enabled: true, percent: 25, account_ids: [8, 90, 7] })
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })

  it('merges the account table selection and preserves checks after changing pages', async () => {
    const wrapper = mountModal()
    await flushPromises()
    const add = wrapper.findAll('button').find(button => button.text().includes('addSelected'))!
    await add.trigger('click')
    await wrapper.get('button[aria-label="common.next"]').trigger('click')
    await flushPromises()
    expect(api.list).toHaveBeenLastCalledWith(2, 25, { search: '', lite: 'true' }, expect.anything())
    await wrapper.get('#cache-read-adjustment-form').trigger('submit')
    await flushPromises()
    expect(api.update).toHaveBeenCalledWith({ enabled: false, percent: 10, account_ids: [8, 90, 12] })
    wrapper.unmount()
  })

  it('blocks invalid percentages and retains the form after a save failure', async () => {
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get('#cache-adjustment-percent').setValue('101')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    await wrapper.get('#cache-read-adjustment-form').trigger('submit')
    expect(api.update).not.toHaveBeenCalled()
    await wrapper.get('#cache-adjustment-percent').setValue('0')
    api.update.mockRejectedValueOnce(new Error('storage unavailable'))
    await wrapper.get('#cache-read-adjustment-form').trigger('submit')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    expect(wrapper.emitted('close')).toBeUndefined()
    expect((wrapper.get('#cache-adjustment-percent').element as HTMLInputElement).value).toBe('0')
    wrapper.unmount()
  })

  it('cannot overwrite settings when the initial load fails', async () => {
    api.get.mockRejectedValueOnce(new Error('offline'))
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').exists()).toBe(true)
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    expect(api.update).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
