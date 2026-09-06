import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AccessBlocksView from '../AccessBlocksView.vue'

const { getSettings, updateSettings, list, add, remove, showError, showSuccess } = vi.hoisted(() => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
  list: vi.fn(),
  add: vi.fn(),
  remove: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api', () => ({
  adminAPI: {
    accessBlocks: { getSettings, updateSettings, list, add, remove },
  },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError, showSuccess }),
}))

vi.mock('@/utils/apiError', () => ({
  extractApiErrorMessage: (_error: unknown, fallback: string) => fallback,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) =>
        key.replace(/\{(\w+)\}/g, (_, token) => String(params?.[token] ?? `{${token}}`)),
    }),
  }
})

const settings = () => ({
  enabled: true,
  login_protection_enabled: true,
  login_failure_threshold: 10,
  login_failure_window_seconds: 600,
  login_temporary_block_seconds: 3600,
  blocked_headers: [],
  panel_blacklist_enabled: false,
  panel_blacklist_threshold: 10,
  panel_blacklist_window_seconds: 600,
})

describe('AccessBlocksView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getSettings.mockResolvedValue(settings())
    list.mockResolvedValue({
      items: [{ ip: '203.0.113.10', remaining_seconds: 120, permanent: false, source: 'login_failures' }],
      total: 1,
      page: 1,
      page_size: 20,
    })
    updateSettings.mockImplementation(async (payload) => payload)
    add.mockResolvedValue(undefined)
    remove.mockResolvedValue(undefined)
  })

  it('loads the policy and renders active blocks inline', async () => {
    const wrapper = mount(AccessBlocksView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
        },
      },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('admin.accessBlocks.title')
    expect(wrapper.text()).toContain('203.0.113.10')
    expect(list).toHaveBeenCalledWith({ page: 1, page_size: 20 })
  })

  it('saves policy without sending blank header rules', async () => {
    const wrapper = mount(AccessBlocksView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
        },
      },
    })
    await flushPromises()
    const addRuleButton = wrapper.findAll('button').find(button => button.text().includes('admin.accessBlocks.addHeader'))
    await addRuleButton?.trigger('click')
    await wrapper.findAll('button').find(button => button.text().includes('admin.accessBlocks.save'))?.trigger('click')
    await flushPromises()

    expect(updateSettings).toHaveBeenCalledWith(expect.objectContaining({ blocked_headers: [] }))
    expect(updateSettings.mock.calls[0]?.[0]).not.toHaveProperty('enabled')
    expect(showSuccess).toHaveBeenCalled()
  })

  it('keeps policy controls usable when the Redis block list cannot be loaded', async () => {
    list.mockRejectedValueOnce(new Error('redis unavailable'))
    const wrapper = mount(AccessBlocksView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
        },
      },
    })
    await flushPromises()

    const saveButton = wrapper.findAll('button').find(button => button.text().includes('admin.accessBlocks.save'))
    expect(saveButton?.attributes('disabled')).toBeUndefined()
    expect(wrapper.text()).toContain('admin.accessBlocks.listUnavailable')
    expect(showError).toHaveBeenCalled()
  })

  it('reports a successful add separately when the following list refresh fails', async () => {
    const wrapper = mount(AccessBlocksView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
        },
      },
    })
    await flushPromises()
    list.mockRejectedValueOnce(new Error('refresh failed'))

    await wrapper.get('input[placeholder="203.0.113.10"]').setValue('203.0.113.44')
    await wrapper.findAll('button').find(button => button.text() === 'admin.accessBlocks.add')?.trigger('click')
    await flushPromises()

    expect(add).toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalledWith('admin.accessBlocks.addSuccess')
    expect(showError).toHaveBeenCalledWith('admin.accessBlocks.listLoadFailed')
    expect(showError).not.toHaveBeenCalledWith('admin.accessBlocks.addFailed')
  })
})
