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
import { Loader2, ShieldAlert } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { AuthLayout } from '../auth-layout'
import { completeSsoLogin } from './api'

type SsoState = 'working' | 'failed'

/**
 * V10 单点登录落地页。
 *
 * 流程：V10 服务端签发一次性票据 -> 浏览器跳到 `/v10/login#<票据>`
 * -> 本页读 fragment -> 换会话 -> 跳控制台。
 *
 * 失败是预期路径之一（票据 60 秒过期、用户重复点击导致重放），
 * 所以失败时不弹错误，而是明确告诉用户回 V10 重新进入。
 */
export function SsoLoginScreen() {
  const { t } = useTranslation()
  const [state, setState] = useState<SsoState>('working')

  useEffect(() => {
    let cancelled = false

    async function run() {
      try {
        const ok = await completeSsoLogin(window.location.hash)
        if (cancelled) {
          return
        }
        if (!ok) {
          setState('failed')
          return
        }
        // 票据已消费，立刻把 fragment 从地址栏抹掉，避免被复制或留在历史里。
        window.history.replaceState(null, '', window.location.pathname)
        window.location.replace('/dashboard')
      } catch {
        if (!cancelled) {
          setState('failed')
        }
      }
    }

    void run()
    return () => {
      cancelled = true
    }
  }, [])

  if (state === 'failed') {
    return (
      <AuthLayout>
        <div className='w-full space-y-6 text-center'>
          <div className='bg-muted mx-auto flex h-16 w-16 items-center justify-center rounded-2xl'>
            <ShieldAlert className='h-8 w-8' aria-hidden='true' />
          </div>
          <div className='space-y-2'>
            <h2 className='text-2xl font-semibold tracking-tight'>
              {t('Sign-in link is no longer valid')}
            </h2>
            <p className='text-muted-foreground text-sm sm:text-base'>
              {t(
                'This link can only be used once and expires within a minute. Please return to the business system and open the console again.'
              )}
            </p>
          </div>
        </div>
      </AuthLayout>
    )
  }

  return (
    <AuthLayout>
      <div className='w-full space-y-6 text-center'>
        <div className='bg-muted mx-auto flex h-16 w-16 items-center justify-center rounded-2xl'>
          <Loader2 className='h-8 w-8 animate-spin' aria-hidden='true' />
        </div>
        <div className='space-y-2'>
          <h2 className='text-2xl font-semibold tracking-tight'>
            {t('Signing you in')}
          </h2>
          <p className='text-muted-foreground text-sm sm:text-base'>
            {t('Verifying your one-time sign-in ticket.')}
          </p>
        </div>
        <p role='status' className='sr-only'>
          {t('Signing you in')}
        </p>
      </div>
    </AuthLayout>
  )
}
