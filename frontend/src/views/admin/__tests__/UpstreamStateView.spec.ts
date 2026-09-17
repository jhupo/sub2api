import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import UpstreamStateView from '../UpstreamStateView.vue'

const mocks = vi.hoisted(() => ({
  settings: vi.fn(),
  save: vi.fn(),
  matrix: vi.fn(),
  setPair: vi.fn(),
  setState: vi.fn(),
  refresh: vi.fn(),
  showSuccess: vi.fn()
}))
vi.mock('@/api/admin/upstreamState', () => ({ upstreamStateApi: mocks }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showSuccess: mocks.showSuccess }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } })
}))

let wrapper: VueWrapper
const settings = {
  enabled: true, ttl_minutes: 40, expected_length: 292,
  auto_replace_enabled: true, webshare_enabled: false, webshare_api_key_configured: false, webshare_country_mode: 'random', webshare_countries: [],
  revision: 'revision', state_revision: 'state-revision', pairs: []
}
const normalRow = {
  id: 'a'.repeat(64), account_id: 42, account_name: 'Account A', model: 'model-a', enabled: true,
  cached: 1, digest: 'redacted1234', checked_at: Date.now(), acquired_at: Date.now(),
  issued_at: Date.now(), upstream_expires_at: Date.now() + 60_000, rotation_at: Date.now() + 30_000,
  observed_length: 292, validation: 'normal' as const
}

async function start() {
  wrapper = mount(UpstreamStateView, {
    global: {
      stubs: {
        AppLayout: { template: '<main><slot /></main>' },
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
        RouterLink: { template: '<a><slot /></a>' }
      }
    }
  })
  await flushPromises()
}

function button(suffix: string) {
  const match = wrapper.findAll('button').find(candidate => candidate.text().endsWith(suffix))
  expect(match).toBeDefined()
  return match!
}

describe('UpstreamStateView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.settings.mockResolvedValue({ ...settings })
    mocks.matrix.mockResolvedValue([{ ...normalRow }])
    mocks.setPair.mockResolvedValue({ ...settings, revision: 'pair-revision', pairs: [{ account_id: 42, model: 'model-a' }] })
    mocks.save.mockImplementation(async value => ({ ...value, revision: 'new', state_revision: 'new-state' }))
    mocks.setState.mockResolvedValue({})
    mocks.refresh.mockResolvedValue({})
  })
  afterEach(() => { wrapper?.unmount(); vi.restoreAllMocks() })

  it('renders redacted cards while keeping configuration on the system settings page', async () => {
    await start()
    expect(wrapper.text()).toContain('Account A')
    expect(wrapper.text()).toContain('redacted1234')
    expect(wrapper.find('[data-testid="state-ttl"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="state-length"]').exists()).toBe(false)
    await wrapper.get('[data-testid="state-list-refresh"]').trigger('click')
    await flushPromises()
    expect(mocks.settings).toHaveBeenCalledTimes(1)
    expect(mocks.matrix).toHaveBeenCalledTimes(2)
  })

  it('keeps a settings load failure visible', async () => {
    mocks.settings.mockRejectedValue(new Error('unavailable'))
    await start()
    expect(wrapper.get('[role="alert"]').text()).toContain('unavailable')
    expect(mocks.save).not.toHaveBeenCalled()
  })

  it('enables a pair from the card switch', async () => {
    mocks.matrix.mockResolvedValue([{ ...normalRow, enabled: false, cached: 0, digest: undefined, validation: 'waiting' }])
    await start()
    await wrapper.get('[role="switch"]').trigger('click')
    await flushPromises()
    expect(mocks.setPair).toHaveBeenCalledWith(expect.objectContaining({ account_id: 42, model: 'model-a' }), true, 'revision')
    expect(mocks.save).not.toHaveBeenCalled()
  })

  it('filters cards through checkbox account and model dropdowns', async () => {
    mocks.matrix.mockResolvedValue([
      { ...normalRow },
      { ...normalRow, account_id: 77, account_name: 'Account B', model: 'model-b' }
    ])
    await start()
    expect(wrapper.get('[data-testid="state-cards"]').text()).toContain('Account A')
    expect(wrapper.get('[data-testid="state-cards"]').text()).toContain('Account B')
    const accountFilter = wrapper.get('[data-testid="state-account-filter"]')
    const accountB = accountFilter.findAll('input[type="checkbox"]')[1]
    await accountB.setValue(true)
    expect(wrapper.get('[data-testid="state-cards"]').text()).not.toContain('Account A')
    expect(wrapper.get('[data-testid="state-cards"]').text()).toContain('Account B')
    const modelFilter = wrapper.get('[data-testid="state-model-filter"]')
    const modelA = modelFilter.findAll('input[type="checkbox"]')[0]
    await modelA.setValue(true)
    expect(wrapper.find('[data-testid="state-cards"]').exists()).toBe(false)
  })

  it('disables pair controls while the global feature is off', async () => {
    mocks.settings.mockResolvedValue({ ...settings, enabled: false })
    await start()
    expect(wrapper.get('[role="switch"]').attributes('disabled')).toBeDefined()
  })

  it('sets an exact-length state through the card dialog', async () => {
    await start()
    await button('.setState').trigger('click')
    await wrapper.get('[data-testid="state-input"]').setValue('a'.repeat(292))
    await wrapper.get('[data-testid="state-submit"]').trigger('click')
    await flushPromises()
    expect(mocks.setState).toHaveBeenCalledWith(42, 'model-a', 'a'.repeat(292))
    expect(mocks.showSuccess).toHaveBeenCalled()
  })

  it('refreshes once and retains an upstream error after refreshing the card', async () => {
    mocks.refresh.mockRejectedValue(new Error('encrypted content could not be verified'))
    await start()
    await button('.refreshNow').trigger('click')
    await flushPromises()
    expect(mocks.refresh).toHaveBeenCalledWith(42, 'model-a')
    expect(wrapper.get('[role="alert"]').text()).toContain('encrypted content could not be verified')
    expect(wrapper.text()).toContain('redacted1234')
  })

  it('marks a state that is 16 bytes longer as extended', async () => {
    mocks.matrix.mockResolvedValue([{ ...normalRow, cached: 0, digest: undefined, observed_length: 308, validation: 'extended' }])
    await start()
    expect(wrapper.text()).toContain('.status.extended')
    expect(wrapper.get('[data-testid="state-cards"]').text()).toContain('308')
  })

})
