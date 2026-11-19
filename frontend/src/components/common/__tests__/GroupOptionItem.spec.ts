import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import GroupOptionItem from '../GroupOptionItem.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ cachedPublicSettings: null }),
}))

describe('GroupOptionItem description layout', () => {
  it('uses current group rate times invitation discount and hides time windows', async () => {
    const wrapper = mount(GroupOptionItem, {
      props: {
        name: 'OpenAI', platform: 'openai', rateMultiplier: 2, userRateMultiplier: 1.6,
        userGroupRate: { group_rate_multiplier: 2, user_rate_multiplier: 1.6, invitee_discount_factor: 0.9 },
        nightRateEnabled: true, nightStart: '00:00', nightEnd: '08:00', nightRateMultiplier: 0.5,
        peakRateEnabled: true, peakStart: '18:00', peakEnd: '22:00', peakRateMultiplier: 2
      },
      global: { stubs: { GroupBadge: true } }
    })
    expect(wrapper.text()).toContain('1.8x')
    expect(wrapper.text()).not.toContain('1.6x')
    expect(wrapper.text()).not.toContain('00:00-08:00')
    expect(wrapper.text()).not.toContain('18:00-22:00')
    await wrapper.setProps({ rateMultiplier: 3, userGroupRate: { group_rate_multiplier: 3, user_rate_multiplier: 1.6, invitee_discount_factor: 0.9 } })
    expect(wrapper.text()).toContain('2.7x')
  })

  it('applies multiline and overflow-safe text styles', () => {
    const description = 'First section\nvery-long-unbroken-description-value-that-must-not-overflow'
    const wrapper = mount(GroupOptionItem, {
      props: {
        name: 'Example group',
        platform: 'openai',
        description,
      },
      global: {
        stubs: {
          GroupBadge: true,
        },
      },
    })

    const descriptionElement = wrapper
      .findAll('span')
      .find((element) => element.text() === description)

    expect(descriptionElement).toBeDefined()
    expect(descriptionElement?.classes()).toContain('whitespace-pre-line')
    expect(descriptionElement?.classes()).toContain('[overflow-wrap:anywhere]')
    expect(descriptionElement?.classes()).toContain('line-clamp-3')
    expect(wrapper.find('[title]').attributes('title')).toBe(description)
  })
})
