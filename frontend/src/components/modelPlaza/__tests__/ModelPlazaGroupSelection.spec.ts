import { nextTick } from 'vue'
import { mount, shallowMount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { ModelPlazaGroup, ModelPlazaResponse } from '@/api/modelPlaza'
import ModelPlazaContent from '../ModelPlazaContent.vue'
import PlazaFilterBar from '../PlazaFilterBar.vue'
import PlazaGroupSection from '../PlazaGroupSection.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isAuthenticated: true })
}))

function group(id: number, name: string, platform: string, rate: number): ModelPlazaGroup {
  return {
    id,
    name,
    description: '',
    platform,
    rate_multiplier: rate,
    peak_rate_enabled: false,
    peak_start: '',
    peak_end: '',
    peak_rate_multiplier: 1,
    is_exclusive: false,
    image_rate_independent: false,
    image_rate_multiplier: 1,
    long_context_pricing_enabled: true,
    models: []
  }
}

const response: ModelPlazaResponse = {
  description: '',
  groups: [
    group(20, 'Anthropic Standard', 'anthropic', 1),
    group(10, 'OpenAI Discount', 'openai', 0.5)
  ]
}

describe('模型广场分组选择', () => {
  it('不提供分组全部选项', () => {
    const wrapper = mount(PlazaFilterBar, {
      props: {
        platforms: ['anthropic', 'openai'],
        groups: response.groups.map((g) => ({
          id: g.id,
          name: g.name,
          platform: g.platform,
          rate: g.rate_multiplier
        })),
        rates: [0.5, 1],
        platform: 'all',
        groupId: 10,
        rate: 'all',
        search: ''
      },
      global: { stubs: { Icon: true, PlatformIcon: true } }
    })

    expect(wrapper.findAll('[data-group-option]')).toHaveLength(2)
    expect(wrapper.findAll('[data-group-option]').map((button) => button.text())).toEqual([
      'Anthropic Standard',
      'OpenAI Discount'
    ])
  })

  it('默认只展示倍率最低的分组', async () => {
    const wrapper = shallowMount(ModelPlazaContent, {
      props: { response, loading: false }
    })
    await nextTick()

    expect(wrapper.findComponent(PlazaFilterBar).props('groupId')).toBe(10)
    const sections = wrapper.findAllComponents(PlazaGroupSection)
    expect(sections).toHaveLength(1)
    expect(sections[0].props('group').id).toBe(10)
  })

  it('切换平台后自动选择该平台的第一个分组', async () => {
    const wrapper = shallowMount(ModelPlazaContent, {
      props: { response, loading: false }
    })
    await nextTick()

    wrapper.findComponent(PlazaFilterBar).vm.$emit('update:platform', 'anthropic')
    await nextTick()

    expect(wrapper.findComponent(PlazaFilterBar).props('groupId')).toBe(20)
    expect(wrapper.findComponent(PlazaGroupSection).props('group').id).toBe(20)
  })
})
