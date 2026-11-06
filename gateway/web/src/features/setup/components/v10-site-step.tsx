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
import { PlugZap } from 'lucide-react'
import type { UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'

import type { SetupFormValues } from '../types'

interface V10SiteStepProps {
  form: UseFormReturn<SetupFormValues>
}

/**
 * 安装向导的 V10 站点步骤（可跳过）。
 *
 * 填了 App ID 就在初始化时顺手建好站点，并把明文密钥回显一次；
 * 留空则整个跳过，之后仍然可以到「V10 运营 → 站点」补建。
 * 做成可选步骤是因为有人可能先只把网关跑起来，晚点再接 V10。
 */
export function V10SiteStep({ form }: V10SiteStepProps) {
  const { t } = useTranslation()

  return (
    <div className='space-y-5'>
      <div className='flex items-start gap-3'>
        <div className='bg-primary/10 text-primary rounded-xl p-2.5'>
          <PlugZap className='size-5' />
        </div>
        <div className='space-y-1'>
          <h3 className='text-base font-semibold'>{t('Connect a V10 site')}</h3>
          <p className='text-muted-foreground text-sm'>
            {t(
              'Optional. Create the site now so the V10 plugin has credentials to sign requests with. You can also do this later from V10 Operations.'
            )}
          </p>
        </div>
      </div>

      <FormField
        control={form.control}
        name='v10AppId'
        rules={{
          validate: (value) => {
            const trimmed = value.trim()
            if (trimmed === '') return true
            if (!/^[A-Za-z0-9_-]{1,64}$/.test(trimmed)) {
              return t('Use 1-64 letters, digits, underscores or hyphens')
            }
            return true
          },
        }}
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('App ID')}</FormLabel>
            <FormControl>
              <Input placeholder='czapi' autoComplete='off' {...field} />
            </FormControl>
            <FormDescription>
              {t(
                'Leave blank to skip. The value becomes the X-V10-App-Id header the plugin sends.'
              )}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />

      <FormField
        control={form.control}
        name='v10SiteName'
        render={({ field }) => (
          <FormItem>
            <FormLabel>{t('Site name')}</FormLabel>
            <FormControl>
              <Input
                placeholder={t('Defaults to the App ID')}
                autoComplete='off'
                {...field}
              />
            </FormControl>
            <FormDescription>
              {t('Only used to label the site in this console.')}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />
    </div>
  )
}