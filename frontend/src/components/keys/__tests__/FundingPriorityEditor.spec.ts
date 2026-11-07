import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import FundingPriorityEditor from '../FundingPriorityEditor.vue'
import type { UserSubscription } from '@/types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

function editor(ids = [11, 12]) {
  return mount(FundingPriorityEditor, {
    props: {
      modelValue: ids,
      subscriptions: [11, 12, 13].map(id => ({ id, plan: { name: 'Same plan' }, expires_at: '2027-01-01T00:00:00Z' }) as UserSubscription)
    },
    global: { stubs: { Icon: true } }
  })
}

describe('FundingPriorityEditor', () => {
  it('moves a card without dropping the remaining order', async () => {
    const wrapper = editor([11, 12, 13])
    await wrapper.findAll('[aria-label="quotaFeatures.moveUp"]')[1]!.trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([[12, 11, 13]])
    expect(wrapper.text()).toContain('#11')
    expect(wrapper.text()).toContain('#12')
  })

  it('only offers unselected cards and appends the new card last', async () => {
    const wrapper = editor()
    expect(wrapper.findAll('option').map(option => option.attributes('value'))).toEqual(['0', '13'])
    await wrapper.get('select').setValue('13')
    await wrapper.findAll('button').at(-1)!.trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([[11, 12, 13]])
  })

  it('removes selected cards while retaining at least one payment source', async () => {
    const wrapper = editor()
    await wrapper.findAll('[aria-label="quotaFeatures.remove"]')[0]!.trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([[12]])
    await wrapper.setProps({ modelValue: [12] })
    expect(wrapper.get('[aria-label="quotaFeatures.remove"]').attributes('disabled')).toBeDefined()
  })
})
