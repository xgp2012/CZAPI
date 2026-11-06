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
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { formatNumber, formatQuota } from '@/lib/format'

import { getOverview } from '../api'

/**
 * V10 运营概览。
 *
 * 只呈现"这一层独有"的数字：站点数、托管账户数、活跃 host、今日入账净额。
 * 上游 new-api 的渠道/模型消耗有自己的仪表盘，不在这里重复。
 */
export function V10Overview() {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['v10', 'overview'],
    queryFn: getOverview,
    retry: false,
  })

  if (query.isLoading) return <LoadingState />
  if (query.isError || !query.data) {
    return <ErrorState title={t('Failed to load overview')} onRetry={query.refetch} />
  }

  const data = query.data
  const stats = [
    {
      key: 'sites',
      title: t('V10 Sites'),
      value: formatNumber(data.site_count),
      description: t('Connected business systems'),
    },
    {
      key: 'accounts',
      title: t('Managed Accounts'),
      value: formatNumber(data.account_count),
      description: t('Accounts created by V10'),
    },
    {
      key: 'hosts',
      title: t('Active Subscriptions'),
      value: `${formatNumber(data.active_host_count)} / ${formatNumber(data.host_count)}`,
      description: t('Active hosts across all plans'),
    },
    {
      key: 'today',
      title: t('Credited Today'),
      value: formatQuota(data.today_delta_quota),
      description: t('Net ledger change since local midnight'),
    },
  ]

  return (
    <div className='grid gap-4 sm:grid-cols-2 xl:grid-cols-4'>
      {stats.map((stat) => (
        <Card key={stat.key}>
          <CardHeader className='pb-2'>
            <CardDescription>{stat.title}</CardDescription>
            <CardTitle className='text-2xl tabular-nums'>{stat.value}</CardTitle>
          </CardHeader>
          <CardContent>
            <p className='text-muted-foreground text-xs'>{stat.description}</p>
          </CardContent>
        </Card>
      ))}

      <Card className='sm:col-span-2 xl:col-span-4'>
        <CardHeader>
          <CardTitle className='text-base'>{t('Ledger Integrity')}</CardTitle>
          <CardDescription>
            {t(
              'Every credit is keyed by (site, ref_type, ref_id), so a V10 retry cannot credit twice.'
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className='text-muted-foreground flex flex-wrap gap-x-8 gap-y-2 text-sm'>
          <span>
            {t('Idempotent records')}: {formatNumber(data.ref_count)}
          </span>
          <span>
            {t('Quota per unit')}: {formatNumber(data.quota_per_unit)}
          </span>
        </CardContent>
      </Card>
    </div>
  )
}
