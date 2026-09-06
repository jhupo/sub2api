import { flushPromises, shallowMount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import PricingEntryCard from '../PricingEntryCard.vue'
import type { PricingFormEntry } from '../types'

const getModelDefaultPricing = vi.hoisted(() => vi.fn())

vi.mock('@/api/admin/channels', () => ({
  default: { getModelDefaultPricing },
}))

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

function createEntry(billingMode: PricingFormEntry['billing_mode'] = 'token'): PricingFormEntry {
  return {
    models: [],
    billing_mode: billingMode,
    input_price: null,
    output_price: null,
    cache_write_price: null,
    cache_read_price: null,
    fast_multiplier: null,
    flex_multiplier: null,
    image_input_price: null,
    image_output_price: null,
    per_request_price: null,
    intervals: [],
    time_pricing: {
      timezone: 'Asia/Shanghai',
      periods: [{ start_time: '09:00', end_time: '12:00', multiplier: '2.00' }],
    },
  }
}

describe('PricingEntryCard time pricing visibility', () => {
  it('is hidden by default', () => {
    const wrapper = shallowMount(PricingEntryCard, {
      props: { entry: createEntry() },
    })

    expect(wrapper.findComponent({ name: 'TimePricingSection' }).exists()).toBe(false)
  })

  it('is shown for token pricing when explicitly enabled', () => {
    const wrapper = shallowMount(PricingEntryCard, {
      props: { entry: createEntry(), enableTimePricing: true },
    })

    expect(wrapper.findComponent({ name: 'TimePricingSection' }).exists()).toBe(true)
  })

  it('is hidden for non-token pricing even when explicitly enabled', () => {
    const wrapper = shallowMount(PricingEntryCard, {
      props: { entry: createEntry('per_request'), enableTimePricing: true },
    })

    expect(wrapper.findComponent({ name: 'TimePricingSection' }).exists()).toBe(false)
  })

  it('clears time periods when changing billing mode', () => {
    const entry = createEntry()
    const wrapper = shallowMount(PricingEntryCard, {
      props: { entry, enableTimePricing: true },
    })

    wrapper.findComponent({ name: 'Select' }).vm.$emit('update:modelValue', 'image')

    expect(wrapper.emitted('update')?.[0]?.[0]).toEqual({
      ...entry,
      billing_mode: 'image',
      intervals: [],
      time_pricing: { timezone: 'Asia/Shanghai', periods: [] },
    })
    expect(entry.time_pricing.periods).toHaveLength(1)
  })
})

describe('PricingEntryCard service tier multipliers', () => {
  it('shows Fast and Flex controls only when explicitly enabled', () => {
    const hidden = shallowMount(PricingEntryCard, { props: { entry: createEntry() } })
    expect(hidden.text()).not.toContain('admin.channels.form.fastMultiplier')

    const shown = shallowMount(PricingEntryCard, {
      props: { entry: createEntry(), enableTierMultipliers: true },
    })
    expect(shown.text()).toContain('admin.channels.form.fastMultiplier')
    expect(shown.text()).toContain('admin.channels.form.flexMultiplier')
  })
})

describe('PricingEntryCard model default pricing preview', () => {
  it('loads the clicked model and shows catalog prices without changing the entry', async () => {
    getModelDefaultPricing.mockResolvedValueOnce({
      found: true,
      input_price: 0.000005,
      output_price: 0.00003,
      cache_read_price: 0.0000005,
    })
    const entry = { ...createEntry(), models: ['gpt-5.6-sol', 'gpt-6'] }
    const wrapper = shallowMount(PricingEntryCard, { props: { entry } })

    wrapper.findComponent({ name: 'ModelTagInput' }).vm.$emit('select:model', 'gpt-6')
    await flushPromises()

    expect(getModelDefaultPricing).toHaveBeenCalledWith('gpt-6')
    expect(wrapper.text()).toContain('gpt-6')
    expect(wrapper.text()).toContain('admin.channels.form.defaultPricingLoaded')
    const priceInputs = wrapper.findAll('input[type="number"]')
    expect(priceInputs[0].attributes('placeholder')).toBe('5')
    expect(priceInputs[1].attributes('placeholder')).toBe('30')
    expect(priceInputs[4].attributes('placeholder')).toBe('0.5')
    expect(wrapper.emitted('update')).toBeUndefined()
  })

  it('ignores a slower response after another model is selected', async () => {
    let resolveFirst!: (value: { found: boolean, input_price: number }) => void
    getModelDefaultPricing
      .mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve }))
      .mockResolvedValueOnce({ found: true, input_price: 0.000002 })
    const entry = { ...createEntry(), models: ['model-a', 'model-b'] }
    const wrapper = shallowMount(PricingEntryCard, { props: { entry } })
    const input = wrapper.findComponent({ name: 'ModelTagInput' })

    input.vm.$emit('select:model', 'model-a')
    input.vm.$emit('select:model', 'model-b')
    await flushPromises()
    resolveFirst({ found: true, input_price: 0.000009 })
    await flushPromises()

    expect(wrapper.text()).toContain('model-b')
    expect(wrapper.findAll('input[type="number"]')[0].attributes('placeholder')).toBe('2')
  })
})
