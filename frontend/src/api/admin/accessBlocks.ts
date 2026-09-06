import { apiClient } from '../client'

export interface AccessBlockedHeaderRule {
  name: string
  value: string
}

export interface AccessBlockSettings {
  enabled: boolean
  login_protection_enabled: boolean
  login_failure_threshold: number
  login_failure_window_seconds: number
  login_temporary_block_seconds: number
  blocked_headers: AccessBlockedHeaderRule[]
  panel_blacklist_enabled: boolean
  panel_blacklist_threshold: number
  panel_blacklist_window_seconds: number
}

export interface AccessBlock {
  ip: string
  remaining_seconds: number
  permanent: boolean
  source: string
}

export interface AccessBlockListResponse {
  items: AccessBlock[]
  total: number
  page: number
  page_size: number
}

export async function getSettings(): Promise<AccessBlockSettings> {
  const { data } = await apiClient.get<AccessBlockSettings>('/admin/access-blocks/settings')
  return data
}

export async function updateSettings(settings: Partial<AccessBlockSettings>): Promise<AccessBlockSettings> {
  const { data } = await apiClient.patch<AccessBlockSettings>('/admin/access-blocks/settings', settings)
  return data
}

export async function list(params: { page?: number; page_size?: number } = {}): Promise<AccessBlockListResponse> {
  const { data } = await apiClient.get<AccessBlockListResponse>('/admin/access-blocks', { params })
  return data
}

export async function add(payload: { ip: string; permanent: boolean; duration_seconds: number }): Promise<void> {
  await apiClient.post('/admin/access-blocks', payload)
}

export async function remove(ip: string): Promise<void> {
  await apiClient.post('/admin/access-blocks/remove', { ip })
}

export const accessBlocksAPI = {
  getSettings,
  updateSettings,
  list,
  add,
  remove,
}

export default accessBlocksAPI
