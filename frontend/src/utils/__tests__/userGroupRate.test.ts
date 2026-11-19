import { describe, expect, it } from 'vitest'
import { formatUserGroupRateWindow, resolveUserGroupRate } from '../userGroupRate'

describe('invited user group rates', () => {
  it.each([
    [0.8, 0.9, [0.5], 0.9],
    [0.4, 0.9, [0.5], 0.9],
    [0.8, 0.9, [0.5, 0.7, 2, 1.2], 0.9],
    [1.5, 1, [0.7], 1],
    [0, 0.9, [0.5, 2], 0.9],
  ] as const)('ignores personal %s when invite %s is configured, regardless of time factors %j', (user, invite, factors, expected) => {
    expect(resolveUserGroupRate({ group_rate_multiplier: 1, user_rate_multiplier: user, invitee_discount_factor: invite }, ...factors)).toBeCloseTo(expected)
  })

  it('follows changes to the current group default instead of storing a fixed registration rate', () => {
    const rate = { group_rate_multiplier: 1, user_rate_multiplier: 0.8, invitee_discount_factor: 0.9 }
    expect(resolveUserGroupRate(rate, 0.5, 2)).toBe(0.9)
    rate.group_rate_multiplier = 2
    expect(resolveUserGroupRate(rate, 0.5, 2)).toBe(1.8)
  })

  it('preserves legacy multiplication without an invitation discount', () => {
    expect(resolveUserGroupRate({ group_rate_multiplier: 2, user_rate_multiplier: 0.8 }, 0.5, 0.7, 2)).toBeCloseTo(0.56)
  })

  it('keeps free groups free', () => {
    expect(resolveUserGroupRate({ group_rate_multiplier: 0, user_rate_multiplier: 1, invitee_discount_factor: 0.9 }, 0.5, 2)).toBe(0)
  })

  it('does not show time window rates for invitation pricing', () => {
    expect(formatUserGroupRateWindow({ peak_rate_enabled: true, peak_start: '00:00', peak_end: '08:00', peak_rate_multiplier: 0.5 }, {
      group_rate_multiplier: 2, user_rate_multiplier: 1.6, invitee_discount_factor: 0.9
    }, 'UTC+08:00')).toBe('')
  })
})
