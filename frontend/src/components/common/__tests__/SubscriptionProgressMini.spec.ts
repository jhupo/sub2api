import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createPinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { useSubscriptionStore } from '@/stores/subscriptions'
import type { UserSubscription } from '@/types'
import SubscriptionProgressMini from '../SubscriptionProgressMini.vue'

describe('SubscriptionProgressMini quota cards', () => {
  for (const mode of ['hours', 'deadline']) {
    it(`shows total quota for ${mode} cards including reserved usage`, async () => {
      const pinia = createPinia()
      const store = useSubscriptionStore(pinia)
      store.activeSubscriptions = [{
        id: 1, expires_at: '2099-01-01T00:00:00Z', total_usage_usd: 20, total_reserved_usd: 5,
        plan: { name: 'Quota card', card_mode: mode, total_limit_usd: 300 },
      } as UserSubscription]
      const wrapper = mount(SubscriptionProgressMini, {
        global: { plugins: [pinia, createI18n({ legacy: false, locale: 'en', messages: { en: {} }, missingWarn: false, fallbackWarn: false })], stubs: { Icon: true, RouterLink: true } },
      })
      await wrapper.get('button').trigger('click')
      expect(wrapper.text()).toContain('$25.00/$300.00')
      expect(wrapper.text()).not.toContain('subscriptionProgress.unlimited')
      wrapper.unmount()
    })
  }
})
