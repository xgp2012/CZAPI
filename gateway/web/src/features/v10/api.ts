/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'

import type {
  Paged,
  V10Account,
  V10Host,
  V10Key,
  V10LedgerRow,
  V10Overview,
  V10Site,
  V10SiteSecret,
  V10UsageModelRow,
  V10UsageRow,
} from './types'

/**
 * V10 运营面客户端。
 *
 * 走 new-api 控制台的既有会话（Cookie + Bearer），因此复用 `api` 实例即可，
 * 不需要另建一套 token 存储。所有接口挂在 /api/v10/console 下，后端同时
 * 认旧 czapi_admin Cookie，旧后台 /admin/ 因此仍可用。
 */
const BASE = '/api/v10/console'

async function unwrap<T>(promise: Promise<{ data: unknown }>): Promise<T> {
  const res = await promise
  const body = requireServerSuccess(res.data) as { data?: T }
  return body.data as T
}

function withQuery(path: string, params: Record<string, unknown>): string {
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue
    search.set(key, String(value))
  }
  const qs = search.toString()
  return qs ? `${path}?${qs}` : path
}

// ---------------------------------------------------------------- 概览

export function getOverview() {
  return unwrap<V10Overview>(api.get(`${BASE}/overview`))
}

// ---------------------------------------------------------------- 站点

export function getSites() {
  return unwrap<Paged<V10Site>>(api.get(`${BASE}/sites`))
}

export function createSite(payload: { app_id: string; name?: string }) {
  return unwrap<V10SiteSecret>(api.post(`${BASE}/sites`, payload))
}

export function rotateSiteSecret(id: number) {
  return unwrap<V10SiteSecret>(api.post(`${BASE}/sites/${id}/secret`))
}

export function setSiteStatus(id: number, enabled: boolean) {
  return unwrap<unknown>(api.post(`${BASE}/sites/${id}/status`, { enabled }))
}

// ---------------------------------------------------------------- 账户

export function getAccounts(params: {
  page?: number
  page_size?: number
  keyword?: string
  site_id?: number
} = {}) {
  return unwrap<Paged<V10Account>>(api.get(withQuery(`${BASE}/accounts`, params)))
}

export function manualCredit(
  userId: number,
  payload: {
    /** 单位是元，正数加额度、负数扣额度。 */
    amount_yuan: number
    remark?: string
    allow_negative?: boolean
  }
) {
  return unwrap<unknown>(api.post(`${BASE}/accounts/${userId}/credit`, payload))
}

// ---------------------------------------------------------------- 账本

export function getLedger(params: {
  page?: number
  page_size?: number
  site_id?: number
  kind?: string
  ref_type?: string
  keyword?: string
  from?: number
  to?: number
} = {}) {
  return unwrap<Paged<V10LedgerRow>>(api.get(withQuery(`${BASE}/ledger`, params)))
}

// ---------------------------------------------------------------- Key

export function getKeys(params: {
  page?: number
  page_size?: number
  keyword?: string
  user_id?: number
} = {}) {
  return unwrap<Paged<V10Key>>(api.get(withQuery(`${BASE}/keys`, params)))
}

export function revokeKey(id: number) {
  return unwrap<unknown>(api.post(`${BASE}/keys/${id}/revoke`))
}

// ---------------------------------------------------------------- Host

export function getHosts(params: {
  page?: number
  page_size?: number
  keyword?: string
  status?: string
} = {}) {
  return unwrap<Paged<V10Host>>(api.get(withQuery(`${BASE}/hosts`, params)))
}

export function setHostStatus(id: number, status: string, reason?: string) {
  return unwrap<unknown>(
    api.post(`${BASE}/hosts/${id}/status`, { status, reason })
  )
}

// ---------------------------------------------------------------- 用量

export function getUsageSummary(params: { from?: number; to?: number } = {}) {
  return unwrap<{ items: V10UsageRow[]; from: number; to: number }>(
    api.get(withQuery(`${BASE}/usage/summary`, params))
  )
}

export function getUsageModels(params: { from?: number; to?: number } = {}) {
  return unwrap<{ items: V10UsageModelRow[]; from: number; to: number }>(
    api.get(withQuery(`${BASE}/usage/models`, params))
  )
}
