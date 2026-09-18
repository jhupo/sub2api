import { apiClient } from '../client'

export interface UpstreamStateSettings {
  enabled: boolean
  auto_replace_enabled: boolean
  ttl_minutes: number
  expected_length: number
  webshare_enabled: boolean
  webshare_api_key?: string
  webshare_api_key_configured: boolean
  webshare_country_mode: 'random' | 'specified'
  webshare_countries: string[]
  revision: string
  state_revision: string
  pairs: { account_id: number; model: string }[]
}

export interface UpstreamStateMatrixRow {
  id?: string
  account_id: number
  account_name: string
  model: string
  enabled: boolean
  cached: number
  state_length: number
  digest?: string
  checked_at: number
  acquired_at: number
  issued_at: number
  upstream_expires_at: number
  rotation_at: number
  observed_length: number
  validation: 'waiting' | 'normal' | 'extended' | 'missing' | 'mismatch' | 'invalid' | 'expired' | 'refresh_error'
  expiry_source?: 'fernet' | 'fallback'
  last_error?: string
  last_refresh_at: number
}

export interface UpstreamStateActionResult {
  account_id: number
  model: string
  length: number
  digest: string
  issued_at: number
  upstream_expires_at: number
  rotation_at: number
  validation: string
  expiry_source?: 'fernet' | 'fallback'
}

const base = '/admin/settings/upstream-state'

export const upstreamStateApi = {
  async matrix(): Promise<UpstreamStateMatrixRow[]> {
    return (await apiClient.get<UpstreamStateMatrixRow[]>(`${base}/matrix`)).data
  },
  async setPair(row: UpstreamStateMatrixRow, enabled: boolean, revision: string): Promise<UpstreamStateSettings> {
    return (await apiClient.put<UpstreamStateSettings>(`${base}/pair`, { account_id: row.account_id, model: row.model, enabled, revision })).data
  },
  async settings(): Promise<UpstreamStateSettings> {
    return (await apiClient.get<UpstreamStateSettings>(base)).data
  },
  async save(settings: UpstreamStateSettings): Promise<UpstreamStateSettings> {
    return (await apiClient.put<UpstreamStateSettings>(base, settings)).data
  },
  async setState(accountId: number, model: string, state: string): Promise<UpstreamStateActionResult> {
    return (await apiClient.put<UpstreamStateActionResult>(`${base}/state`, { account_id: accountId, model, state })).data
  },
  async refresh(accountId: number, model: string): Promise<UpstreamStateActionResult> {
    return (await apiClient.post<UpstreamStateActionResult>(`${base}/refresh`, { account_id: accountId, model }, { timeout: 30000 })).data
  }
}
