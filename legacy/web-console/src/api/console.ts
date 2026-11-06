/**
 * User-centre API (`/console/v1`) — docs/console-api-v1.md §1.
 * Each function dispatches to the mock layer unless VITE_USE_MOCK=false.
 */

import { get, isMock, patch, post, del } from './client'
import { mockConsole } from './mock'
import type {
  ApiKey,
  ConsoleModel,
  CreateKeyRequest,
  CreateKeyResponse,
  DocsPayload,
  Overview,
  Paged,
  Totalled,
  UpdateKeyRequest,
  UsageGroupBy,
  UsageLog,
  UsageLogQuery,
  UsageSummary,
} from '@/types/api'

export const consoleApi = {
  /** §1.2 */
  async overview(): Promise<Overview> {
    if (isMock()) return mockConsole.overview()
    return get<Overview>('/console/v1/overview')
  },

  /** §1.3 */
  async listKeys(): Promise<ApiKey[]> {
    if (isMock()) return mockConsole.listKeys()
    const data = await get<Paged<ApiKey> | { items: ApiKey[] }>('/console/v1/keys')
    return 'items' in data ? data.items : []
  },

  /** §1.3 — the plaintext key is returned exactly once. */
  async createKey(payload: CreateKeyRequest): Promise<CreateKeyResponse> {
    if (isMock()) return mockConsole.createKey(payload)
    return post<CreateKeyResponse>('/console/v1/keys', payload)
  },

  /** §1.3 */
  async updateKey(id: number, payload: UpdateKeyRequest): Promise<ApiKey> {
    if (isMock()) return mockConsole.updateKey(id, payload as Partial<ApiKey>)
    return patch<ApiKey>(`/console/v1/keys/${id}`, payload)
  },

  /** §1.3 — idempotent revoke (status → revoked). */
  async revokeKey(id: number): Promise<{ key_id: number; status: ApiKey['status'] }> {
    if (isMock()) return mockConsole.revokeKey(id)
    return del<{ key_id: number; status: ApiKey['status'] }>(`/console/v1/keys/${id}`)
  },

  /** §1.4 */
  async models(): Promise<Totalled<ConsoleModel>> {
    if (isMock()) return mockConsole.models()
    return get<Totalled<ConsoleModel>>('/console/v1/models')
  },

  /** §1.5 */
  async usageSummary(from: number, to: number, group_by: UsageGroupBy = 'day'): Promise<UsageSummary> {
    if (isMock()) return mockConsole.usageSummary(from, to, group_by)
    return get<UsageSummary>('/console/v1/usage/summary', { params: { from, to, group_by } })
  },

  /** §1.5 — no prompt/response bodies are ever returned. */
  async usageLogs(query: UsageLogQuery): Promise<Paged<UsageLog>> {
    if (isMock()) return mockConsole.usageLogs(query)
    return get<Paged<UsageLog>>('/console/v1/usage/logs', { params: query })
  },

  /** §1.5 — CSV export shares the logs query but returns text/csv. */
  async usageExport(query: Omit<UsageLogQuery, 'page' | 'page_size'>): Promise<string> {
    if (isMock()) {
      const data = await mockConsole.usageLogs({ ...query, page: 1, page_size: 200 })
      const header = 'request_id,model,prompt_tokens,completion_tokens,cost_micros,billing_mode,status_code,latency_ms,ttft_ms,created_at'
      const lines = data.items.map((r) =>
        [r.request_id, r.model, r.prompt_tokens, r.completion_tokens, r.cost_micros, r.billing_mode, r.status_code, r.latency_ms, r.ttft_ms, r.created_at].join(','),
      )
      return [header, ...lines, '', `# 共 ${data.total} 条（mock 导出上限 200 条）`].join('\n')
    }
    const res = await fetch(`/console/v1/usage/export?${new URLSearchParams(query as Record<string, string>)}`, {
      credentials: 'include',
    })
    if (!res.ok) throw new Error(`导出失败（HTTP ${res.status}）`)
    return res.text()
  },

  /** §1.6 */
  async docs(): Promise<DocsPayload> {
    if (isMock()) return mockConsole.docs()
    return get<DocsPayload>('/console/v1/docs')
  },
}
