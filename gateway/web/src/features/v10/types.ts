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

/** V10 运营面（原 admin-ui）共用类型。 */

/** 后端统一信封：{ success, message, data }。 */
export type ApiEnvelope<T> = {
  success: boolean
  message?: string
  data?: T
}

export type Paged<T> = {
  items: T[]
  total: number
  page?: number
  page_size?: number
}

/** 站点：与 /internal/v10 的 app_id/secret 对应。 */
export type V10Site = {
  id: number
  app_id: string
  name: string
  secret_prefix: string
  status: number
  created_at: number
}

/** 新建或轮换后返回的明文密钥，只出现一次。 */
export type V10SiteSecret = {
  id: number
  app_id: string
  name: string
  secret: string
  warning?: string
  notice?: string
}

export type V10Account = {
  user_id: number
  client_id: number
  site_id: number
  username: string
  quota: number
  used_quota: number
  created_at: number
}

export type V10LedgerRow = {
  id: number
  site_id: number
  user_id: number
  kind: string
  ref_type: string
  ref_id: string
  delta_quota: number
  balance_after: number
  remark: string
  created_at: number
}

export type V10Host = {
  id: number
  site_id: number
  host_id: number
  user_id: number
  client_id: number
  user_subscription_id: number
  plan_id: number
  host_status: string
  created_at: number
  updated_at: number
}

export type V10Key = {
  id: number
  user_id: number
  username: string
  client_id: number
  name: string
  status: number
  used_quota: number
  expired_time: number
  created_time: number
  unlimited_quota: boolean
}

export type V10Overview = {
  site_count: number
  account_count: number
  host_count: number
  active_host_count: number
  today_delta_quota: number
  ref_count: number
  quota_per_unit: number
}

/** 按客户聚合的消费行（/usage/summary）。 */
export type V10UsageRow = {
  user_id: number
  client_id: number
  username: string
  quota: number
  request_count: number
  prompt_tokens: number
  completion_tokens: number
}

/** 按模型聚合的消费行（/usage/models）。 */
export type V10UsageModelRow = {
  model_name: string
  quota: number
  request_count: number
  prompt_tokens: number
  completion_tokens: number
}
