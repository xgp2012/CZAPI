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
import { applyAuthBundle, isAuthBundle } from '@/lib/auth-session'

import type { SsoExchangeResponse } from './types'

/**
 * 从 URL fragment 取出一次性票据。
 *
 * 票据必须走 fragment 而不是 query string：fragment 不会发给服务器，
 * 因此不会进入 nginx/网关的访问日志，也不会出现在 Referer 里。
 *
 * V10 拼出的地址形如 `https://console.example.com/v10/login#<票据>`。
 */
export function readTicketFromHash(hash: string): string {
  const raw = hash.startsWith('#') ? hash.slice(1) : hash
  if (raw === '') {
    return ''
  }

  // 兼容 `#ticket=xxx` 的写法，但主要形式是裸票据。
  const params = new URLSearchParams(raw)
  const explicit = params.get('ticket')
  if (explicit !== null && explicit !== '') {
    return explicit
  }

  // 裸票据里不应出现 `=`；出现说明不是我们签发的内容，直接拒绝。
  if (raw.includes('=')) {
    return ''
  }

  return raw
}

/**
 * 用一次性票据换取会话。
 *
 * 这个接口不需要签名：调用方是浏览器，拿不到 app_secret。
 * 安全性来自票据本身——60 秒过期、只能消费一次、库里只存哈希。
 */
export async function exchangeTicket(
  ticket: string,
  appId?: string
): Promise<SsoExchangeResponse> {
  const res = await api.post<SsoExchangeResponse>(
    '/api/v10/sso/exchange',
    { ticket, ...(appId ? { app_id: appId } : {}) },
    { skipAuthRefresh: true, skipErrorHandler: true }
  )
  return res.data
}

/**
 * 走完整个单点登录流程：读票据 -> 换取会话 -> 写入本地认证状态。
 *
 * 成功后返回 true；票据缺失、过期、被重放或响应格式不对都返回 false，
 * 由调用方决定展示什么。这里不抛异常，因为失败是预期路径之一
 * （用户在 V10 点了两次"进入控制台"就会撞上一次重放）。
 */
export async function completeSsoLogin(
  hash: string,
  appId?: string
): Promise<boolean> {
  const ticket = readTicketFromHash(hash)
  if (ticket === '') {
    return false
  }

  const payload = await exchangeTicket(ticket, appId)
  if (!payload.success || !isAuthBundle(payload.data)) {
    return false
  }

  applyAuthBundle(payload.data)
  return true
}
