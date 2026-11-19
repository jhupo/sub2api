import type { UserGroupRate } from '@/api/groups'
import { formatPeakRateWindow, type PeakRateFields } from './peak-rate'

export function hasInviteeDiscount(rate?: UserGroupRate | null): boolean {
  return rate?.invitee_discount_factor != null && rate.invitee_discount_factor > 0 && rate.invitee_discount_factor <= 1
}

/** Invitation rates follow the current group rate, independent of other offers. */
export function resolveUserGroupRate(rate: UserGroupRate, ...factors: number[]): number {
  if (!hasInviteeDiscount(rate)) {
    return factors.reduce((value, factor) => value * factor, rate.user_rate_multiplier ?? rate.group_rate_multiplier)
  }
  return rate.group_rate_multiplier * rate.invitee_discount_factor!
}

/** Time windows do not affect invitation rates. */
export function formatUserGroupRateWindow(fields: PeakRateFields, rate?: UserGroupRate | null, timezone?: string): string {
  return hasInviteeDiscount(rate) ? '' : formatPeakRateWindow(fields, timezone)
}
