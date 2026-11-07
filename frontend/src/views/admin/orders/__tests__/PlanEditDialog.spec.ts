import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'

import PlanEditDialog from '../PlanEditDialog.vue'
import PlanSaleEditor from '@/components/payment/PlanSaleEditor.vue'
import { adminPaymentAPI } from '@/api/admin/payment'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      if (key === 'payment.admin.subscriptionCnyPayPreview') return `preview ${params?.amount}`
      if (key === 'payment.admin.subscriptionCnyPayPreviewWithFee') return `fee ${params?.feeRate} ${params?.total}`
      return key
    },
  }),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
  }),
}))

vi.mock('@/api/admin/payment', () => ({
  adminPaymentAPI: {
    createPlan: vi.fn(),
    updatePlan: vi.fn(),
  },
}))

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: {
    show: Boolean,
    title: String,
    width: String,
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

const SelectStub = defineComponent({
  name: 'SelectStub',
  props: {
    modelValue: [String, Number],
    options: {
      type: Array,
      default: () => [],
    },
    placeholder: String,
  },
  emits: ['update:modelValue'],
  setup(_props, { emit }) {
    const onChange = (event: Event) => {
      const value = (event.target as HTMLSelectElement).value
      emit('update:modelValue', value === '' ? null : Number(value))
    }
    return { onChange }
  },
  template: `
    <select
      :value="modelValue ?? ''"
      @change="onChange"
    >
      <option value="">{{ placeholder }}</option>
      <option
        v-for="option in options"
        :key="option.value"
        :value="option.value"
        :data-platform="option.platform"
      >
        {{ option.label }}
      </option>
    </select>
  `,
})

function mountDialog({ paymentConfig = null }: { paymentConfig?: Record<string, unknown> | null } = {}) {
  return mount(PlanEditDialog, {
    props: {
      show: true,
      plan: null,
      paymentConfig,
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Select: SelectStub,
        Icon: true,
      },
    },
  })
}

describe('PlanEditDialog', () => {
  it('publishes multiple scheduled prices and a cross-midnight daily sale window', async () => {
    const wrapper = mountDialog()
    await wrapper.get('select').setValue('hours')
    const inputs = wrapper.findAll('input[type="number"]')
    await inputs[0]!.setValue('45')
    await inputs[2]!.setValue('300')
    await inputs[3]!.setValue('12')
    const sale = wrapper.getComponent(PlanSaleEditor)
    await sale.get('input[type="datetime-local"]').setValue('2027-01-03T12:00')
    await sale.findAll('button')[0]!.trigger('click')
    await sale.findAll('input[type="datetime-local"]')[1]!.setValue('2027-01-01T20:00')
    await sale.get('input[type="number"]').setValue('35')
    await sale.findAll('button')[0]!.trigger('click')
    await sale.findAll('input[type="datetime-local"]')[2]!.setValue('2027-01-02T20:00')
    await sale.findAll('input[type="number"]')[1]!.setValue('25')
    const addButtons = sale.findAll('button').filter(button => button.text().includes('common.add'))
    await addButtons[1]!.trigger('click')
    await wrapper.get('#plan-form').trigger('submit')
    expect(adminPaymentAPI.createPlan).toHaveBeenLastCalledWith(expect.objectContaining({
      price: 45,
      sale_schedule: {
        ends_at: new Date('2027-01-03T12:00').toISOString(),
        price_steps: [{ at: new Date('2027-01-01T20:00').toISOString(), price: 35 }, { at: new Date('2027-01-02T20:00').toISOString(), price: 25 }],
        daily_windows: [{ start: '22:00', end: '08:00' }],
      },
    }))
    wrapper.unmount()
  })
  it('shows CNY channel charge using the configured subscription rate and fee', async () => {
    const wrapper = mountDialog({
      paymentConfig: {
        subscription_usd_to_cny_rate: 7.15,
        recharge_fee_rate: 2.5,
      },
    })

    await wrapper.find('input[type="number"]').setValue('9.99')

    expect(wrapper.text()).toContain('preview')
    expect(wrapper.text()).toContain('¥71.43')
    expect(wrapper.text()).toContain('fee 2.5')
    expect(wrapper.text()).toContain('¥73.22')
  })

  it('hides the preview when the subscription rate is not configured', async () => {
    const wrapper = mountDialog({
      paymentConfig: {
        subscription_usd_to_cny_rate: 0,
        recharge_fee_rate: 2.5,
      },
    })

    await wrapper.find('input[type="number"]').setValue('9.99')

    expect(wrapper.text()).not.toContain('preview')
    expect(wrapper.text()).not.toContain('¥71.43')
  })

  it('publishes the configured allowance limits with the plan', async () => {
    const wrapper = mountDialog()
    const inputs = wrapper.findAll('input[type="number"]')

    await inputs[0].setValue('19.99')
    await inputs[2].setValue('30')
    await inputs[3].setValue('50')
    await inputs[4].setValue('250')
    await inputs[5].setValue('900')
    await wrapper.get('#plan-form').trigger('submit')

    expect(adminPaymentAPI.createPlan).toHaveBeenCalledWith(
      expect.objectContaining({
        price: 19.99,
        validity_days: 30,
        daily_limit_usd: 50,
        weekly_limit_usd: 250,
        monthly_limit_usd: 900,
      }),
    )
  })

  it('publishes an hourly card with a lifetime allowance and no recurring refill', async () => {
    const wrapper = mountDialog()
    await wrapper.get('select').setValue('hours')
    const inputs = wrapper.findAll('input[type="number"]')
    await inputs[0]!.setValue('9.99')
    await inputs[2]!.setValue('300')
    await inputs[3]!.setValue('12')
    await wrapper.get('#plan-form').trigger('submit')
    expect(adminPaymentAPI.createPlan).toHaveBeenLastCalledWith(expect.objectContaining({
      card_mode: 'hours', validity_hours: 12, deadline_at: null, total_limit_usd: 300,
      daily_limit_usd: null, weekly_limit_usd: null, monthly_limit_usd: null
    }))
  })

  it('publishes a fixed deadline as an absolute timestamp', async () => {
    const wrapper = mountDialog()
    await wrapper.get('select').setValue('deadline')
    await wrapper.findAll('input[type="number"]')[0]!.setValue('9.99')
    await wrapper.get('input[type="datetime-local"]').setValue('2027-01-02T18:30')
    await wrapper.findAll('input[type="number"]')[2]!.setValue('200')
    await wrapper.get('#plan-form').trigger('submit')
    expect(adminPaymentAPI.createPlan).toHaveBeenLastCalledWith(expect.objectContaining({
      card_mode: 'deadline', validity_hours: 0, deadline_at: new Date('2027-01-02T18:30').toISOString(), total_limit_usd: 200
    }))
  })
})
