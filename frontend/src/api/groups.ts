/**
 * User Groups API endpoints (non-admin)
 * Handles group-related operations for regular users
 */

import { apiClient } from './client'
import type { Group } from '@/types'

export interface UserGroupRate {
  group_rate_multiplier: number
  user_rate_multiplier?: number
  invitee_discount_factor?: number
}

/**
 * Get available groups that the current user can bind to API keys
 * This returns groups based on user's permissions:
 * - Standard groups: public (non-exclusive) or explicitly allowed
 * - Subscription groups: user has active subscription
 * @returns List of available groups
 */
export async function getAvailable(): Promise<Group[]> {
  const { data } = await apiClient.get<Group[]>('/groups/available')
  return data
}

/**
 * Get current user's custom group rate multipliers
 * @returns Map of group_id to custom rate_multiplier
 */
export async function getUserGroupRates(): Promise<Record<number, number>> {
  const { data } = await apiClient.get<Record<number, number> | null>('/groups/rates')
  return data || {}
}

export async function getUserGroupRateDetails(): Promise<Record<number, UserGroupRate>> {
  const { data } = await apiClient.get<Record<number, UserGroupRate> | null>('/groups/rate-details')
  return data || {}
}

export const userGroupsAPI = {
  getAvailable,
  getUserGroupRates,
  getUserGroupRateDetails
}

export default userGroupsAPI
