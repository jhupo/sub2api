import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const { appStore, getUpdateStatus, performUpdate } = vi.hoisted(() => ({
  appStore: {
    versionLoading: false,
    currentVersion: '0.1.241',
    latestVersion: '0.1.242',
    hasUpdate: true,
    buildType: 'release',
    releaseInfo: null,
    fetchVersion: vi.fn(),
    clearVersionCache: vi.fn()
  },
  getUpdateStatus: vi.fn(),
  performUpdate: vi.fn()
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => ({ isAdmin: true }),
  useAppStore: () => appStore
}))

vi.mock('@/api/admin/system', () => ({
  performUpdate,
  getUpdateStatus,
  restartService: vi.fn(),
  getRollbackVersions: vi.fn(),
  rollback: vi.fn()
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copied: false, copyToClipboard: vi.fn() })
}))

import VersionBadge from '../VersionBadge.vue'

let wrapper: VueWrapper | undefined

async function startPendingUpdate() {
  wrapper = mount(VersionBadge, {
    global: {
      stubs: {
        Icon: true,
        transition: false
      }
    }
  })
  await wrapper.find('button').trigger('click')
  const updateButton = wrapper.findAll('button').find((button) =>
    button.text().includes('version.updateNow')
  )
  expect(updateButton).toBeDefined()
  await updateButton!.trigger('click')
}

describe('VersionBadge authoritative rollout status', () => {
  beforeEach(() => {
    appStore.fetchVersion.mockReset().mockResolvedValue(undefined)
    appStore.clearVersionCache.mockReset()
    performUpdate.mockReset().mockResolvedValue({
      message: 'scheduled',
      need_restart: false,
      pending: true,
      operation_id: 'sysop-update123'
    })
    getUpdateStatus.mockReset()
  })

  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
    vi.useRealTimers()
  })

  it('keeps succeeded status when the follow-up version refresh fails', async () => {
    getUpdateStatus.mockResolvedValue({ status: 'succeeded' })
    appStore.fetchVersion.mockImplementation((force?: boolean) =>
      force ? Promise.reject(new Error('temporary version error')) : Promise.resolve(undefined)
    )

    await startPendingUpdate()
    await flushPromises()

    expect(wrapper!.text()).toContain('version.updateComplete')
    expect(wrapper!.text()).not.toContain('version.updateFailed')
  })

  it.each([
    ['rolled_back', 'version.rolloutRolledBack'],
    ['failed', 'version.rolloutFailed']
  ])('shows %s as an update failure', async (status, message) => {
    getUpdateStatus.mockResolvedValue({ status })

    await startPendingUpdate()
    await flushPromises()

    expect(wrapper!.text()).toContain('version.updateFailed')
    expect(wrapper!.text()).toContain(message)
  })

  it('keeps polling past the previous timeout while the rollout lease is active', async () => {
    vi.useFakeTimers()
    let status = 'pending'
    getUpdateStatus.mockImplementation(() => Promise.resolve({ status }))

    await startPendingUpdate()
    await vi.advanceTimersByTimeAsync(13 * 60 * 1000)
    await flushPromises()

    expect(wrapper!.text()).not.toContain('version.updateFailed')

    status = 'succeeded'
    await vi.advanceTimersByTimeAsync(2000)
    await flushPromises()

    expect(wrapper!.text()).toContain('version.updateComplete')
  })
})
