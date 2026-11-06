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
import { createFileRoute, redirect } from '@tanstack/react-router'

import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

/**
 * V10 运营面的公共守卫。
 *
 * 抽成 helper 而不是复制六份 `beforeLoad`：这六页的可见性规则完全相同
 * （管理员及以上），将来要加一道鉴权检查也只需要改这里。
 */
export function requireV10Admin() {
  const { auth } = useAuthStore.getState()
  if (!auth.user || auth.user.role < ROLE.ADMIN) {
    throw redirect({ to: '/403' })
  }
}

export const Route = createFileRoute('/_authenticated/v10')({
  beforeLoad: requireV10Admin,
})