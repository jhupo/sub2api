import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ModelTagInput from '../ModelTagInput.vue'

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

describe('ModelTagInput selection', () => {
  it('emits selection from the model button and keeps removal independent', async () => {
    const wrapper = mount(ModelTagInput, {
      props: {
        models: ['model-a', 'model-b'],
        selectable: true,
        selectedModel: 'model-a',
      },
      global: { stubs: { Icon: true } },
    })

    const modelButtons = wrapper.findAll('button[aria-pressed]')
    expect(modelButtons).toHaveLength(2)
    expect(modelButtons[0].attributes('aria-pressed')).toBe('true')

    await modelButtons[1].trigger('click')
    expect(wrapper.emitted('select:model')).toEqual([['model-b']])

    const removeButtons = wrapper.findAll('button:not([aria-pressed])')
    await removeButtons[1].trigger('click')
    expect(wrapper.emitted('update:models')).toEqual([[['model-a']]])
    expect(wrapper.emitted('select:model')).toEqual([['model-b']])
  })
})
