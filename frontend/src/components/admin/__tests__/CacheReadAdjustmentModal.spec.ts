import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import CacheReadAdjustmentModal from '../CacheReadAdjustmentModal.vue'
vi.mock('../CacheReadAdjustmentStats.vue', () => ({ default: { template: '<div>daily stats</div>' } }))

const api = vi.hoisted(() => ({ get: vi.fn(), patch: vi.fn(), list: vi.fn(), selected: vi.fn(), add: vi.fn(), remove: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: {
  settings: {
    getCacheReadAdjustmentSettings: api.get, patchCacheReadAdjustmentSettings: api.patch,
    getCacheReadAdjustmentAccounts: api.selected, addCacheReadAdjustmentAccounts: api.add,
    removeCacheReadAdjustmentAccounts: api.remove
  },
  accounts: { list: api.list }
} }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const initial = { enabled: false, percent: 10, account_ids: [8, 90] }
const account = (id: number) => ({ id, name: `Account ${id}`, platform: 'openai', missing: false })
const selectedPage = (ids: number[], page = 1, pages = 1) => ({ items: ids.map(account), total: ids.length, page, pages })
const mountModal = () => mount(CacheReadAdjustmentModal, {
  props: { show: true, selectedIds: [12] },
  global: { stubs: { BaseDialog: { template: '<section><slot /><slot name="footer" /></section>' } } }
})

describe('CacheReadAdjustmentModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.get.mockResolvedValue({ ...initial })
    api.list.mockResolvedValue({ items: [account(7), account(8)], total: 30 })
    api.selected.mockResolvedValue(selectedPage([8, 90]))
    api.patch.mockImplementation(async patch => ({ ...initial, ...patch }))
    api.add.mockImplementation(async ids => ({ ...initial, account_ids: [...initial.account_ids, ...ids] }))
    api.remove.mockImplementation(async ids => ({ ...initial, account_ids: initial.account_ids.filter(id => !ids.includes(id)) }))
  })

  it('persists the switch immediately without submitting an unsaved percentage or closing', async () => {
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get('#cache-adjustment-percent').setValue('25')
    await wrapper.get('#cache-adjustment-enabled').trigger('click')
    await flushPromises()
    expect(api.patch).toHaveBeenCalledTimes(1)
    expect(api.patch).toHaveBeenCalledWith({ enabled: true })
    expect(wrapper.get('#cache-adjustment-enabled').attributes('aria-checked')).toBe('true')
    expect((wrapper.get('#cache-adjustment-percent').element as HTMLInputElement).value).toBe('25')
    expect(wrapper.emitted('close')).toBeUndefined()
    wrapper.unmount()
  })

  it('keeps the switch unchanged and reports a failed save', async () => {
    api.patch.mockRejectedValueOnce(new Error('offline'))
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get('#cache-adjustment-enabled').trigger('click')
    await flushPromises()
    expect(wrapper.get('#cache-adjustment-enabled').attributes('aria-checked')).toBe('false')
    expect(wrapper.get('[role="alert"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('shows included accounts independently of search results and adds explicitly', async () => {
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.get('[data-selected-account="90"]').text()).toContain('Account 90')
    expect(wrapper.get('[data-search-account="8"] button').attributes('disabled')).toBeDefined()
    api.selected.mockResolvedValueOnce(selectedPage([7, 8, 90]))
    await wrapper.get('[data-search-account="7"] button').trigger('click')
    await flushPromises()
    expect(api.add).toHaveBeenCalledWith([7])
    expect(wrapper.find('[data-selected-account="7"]').exists()).toBe(true)
    await wrapper.get('button[aria-label="common.next"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-selected-account="90"]').exists()).toBe(true)
    expect(api.list).toHaveBeenLastCalledWith(2, 25, { search: '', lite: 'true' }, expect.anything())
    wrapper.unmount()
  })

  it('can add accounts selected in the main account table', async () => {
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text().includes('addSelected'))!.trigger('click')
    await flushPromises()
    expect(api.add).toHaveBeenCalledTimes(1)
    expect(api.add).toHaveBeenCalledWith([12])
    wrapper.unmount()
  })

  it('retains removal checks across pages and submits only checked IDs', async () => {
    api.selected.mockResolvedValueOnce(selectedPage([8], 1, 2))
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get('#cache-selected-8').setValue(true)
    api.selected.mockResolvedValueOnce(selectedPage([90], 2, 2))
    await wrapper.get('button[aria-label="admin.accounts.cacheReadAdjustment.nextSelected"]').trigger('click')
    await flushPromises()
    await wrapper.get('#cache-selected-90').setValue(true)
    api.selected.mockResolvedValueOnce(selectedPage([]))
    await wrapper.findAll('button').find(button => button.text().includes('removeSelected'))!.trigger('click')
    await flushPromises()
    expect(api.remove).toHaveBeenCalledTimes(1)
    expect(api.remove).toHaveBeenCalledWith([8, 90])
    expect(wrapper.findAll('[data-selected-account]')).toHaveLength(0)
    wrapper.unmount()
  })

  it('saves only the percentage and blocks invalid values', async () => {
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get('#cache-adjustment-percent').setValue('101')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    await wrapper.get('#cache-read-adjustment-form').trigger('submit')
    expect(api.patch).not.toHaveBeenCalled()
    await wrapper.get('#cache-adjustment-percent').setValue('0')
    await wrapper.get('#cache-read-adjustment-form').trigger('submit')
    await flushPromises()
    expect(api.patch).toHaveBeenCalledTimes(1)
    expect(api.patch).toHaveBeenCalledWith({ percent: 0 })
    wrapper.unmount()
  })

  it('cannot mutate settings when the initial load fails', async () => {
    api.get.mockRejectedValueOnce(new Error('offline'))
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').exists()).toBe(true)
    expect(wrapper.find('#cache-adjustment-enabled').exists()).toBe(false)
    expect(api.patch).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
