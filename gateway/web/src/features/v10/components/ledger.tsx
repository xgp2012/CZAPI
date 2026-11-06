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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Badge } from '@/components/ui/badge'
import { formatQuota } from '@/lib/format'

import { getLedger, getSites } from '../api'
import type { V10LedgerRow } from '../types'
import { V10FilterBar, V10Pager } from './filter-bar'
import { formatUnixDateTime } from '../lib/format-time'

const PAGE_SIZE = 20

/**
 * 账本对账。
 *
 * 这张表是"V10 重推不会重复加钱"这一承诺的证据链：每行都带
 * (site_id, ref_type, ref_id)，三者唯一。差异排查从 ref_id 开始。
 */
export function V10Ledger() {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)
  const [keyword, setKeyword] = useState('')
  const [kind, setKind] = useState('')

  const sitesQuery = useQuery({
    queryKey: ['v10', 'sites'],
    queryFn: getSites,
    retry: false,
  })

  const query = useQuery({
    queryKey: ['v10', 'ledger', page, keyword, kind],
    queryFn: () =>
      getLedger({ page, page_size: PAGE_SIZE, keyword, kind: kind || undefined }),
    retry: false,
  })

  if (query.isLoading) return <LoadingState />
  if (query.isError) return <ErrorState onRetry={query.refetch} />

  const items = query.data?.items ?? []
  const total = query.data?.total ?? 0
  const siteNames = new Map(
    (sitesQuery.data?.items ?? []).map((site) => [site.id, site.app_id])
  )

  // 与 V10 插件 LlmApi::creditAccount 接受的枚举保持一致：
  // v10_order | redeem_code | admin_adjust | refund。写错的值会让过滤永远空表。
  const kindOptions = [
    { value: 'v10_order', label: t('V10 order') },
    { value: 'redeem_code', label: t('Redeem code') },
    { value: 'admin_adjust', label: t('Admin adjust') },
    { value: 'refund', label: t('Refund') },
  ]

  return (
    <div className='space-y-3'>
      <V10FilterBar
        keyword={keyword}
        onKeywordChange={setKeyword}
        onSearch={() => setPage(1)}
        placeholder={t('Search by reference ID')}
        filter={{
          value: kind,
          onChange: (value) => {
            setKind(value)
            setPage(1)
          },
          placeholder: t('All kinds'),
          options: kindOptions,
        }}
      />

      {items.length === 0 ? (
        <EmptyState
          bordered
          title={t('No ledger entries')}
          description={t(
            'Entries appear when V10 credits an account or opens a subscription.'
          )}
        />
      ) : (
        <>
          <StaticDataTable<V10LedgerRow>
            data={items}
            getRowKey={(row) => row.id}
            columns={[
              {
                id: 'id',
                header: 'ID',
                className: 'w-16',
                cell: (row) => row.id,
              },
              {
                id: 'site',
                header: t('Site'),
                className: 'w-24',
                cell: (row) => (
                  <span className='font-mono text-xs'>
                    {siteNames.get(row.site_id) ?? row.site_id}
                  </span>
                ),
              },
              {
                id: 'user',
                header: t('User ID'),
                className: 'w-20',
                cell: (row) => (
                  <span className='font-mono text-xs'>{row.user_id}</span>
                ),
              },
              {
                id: 'kind',
                header: t('Kind'),
                className: 'w-36',
                cell: (row) => (
                  <Badge variant='outline' className='font-mono text-xs'>
                    {row.kind}
                  </Badge>
                ),
              },
              {
                id: 'ref',
                header: t('Reference'),
                cell: (row) => (
                  <div className='flex flex-col'>
                    <span className='font-mono text-xs'>{row.ref_id}</span>
                    <span className='text-muted-foreground text-xs'>
                      {row.ref_type}
                    </span>
                  </div>
                ),
              },
              {
                id: 'delta',
                header: t('Delta'),
                className: 'w-32',
                cell: (row) => (
                  <span
                    className={
                      row.delta_quota >= 0
                        ? 'text-success tabular-nums'
                        : 'text-destructive tabular-nums'
                    }
                  >
                    {row.delta_quota >= 0 ? '+' : ''}
                    {formatQuota(row.delta_quota)}
                  </span>
                ),
              },
              {
                id: 'created_at',
                header: t('Time'),
                className: 'w-44',
                cell: (row) => (
                  <span className='text-muted-foreground text-xs'>
                    {formatUnixDateTime(row.created_at)}
                  </span>
                ),
              },
            ]}
          />
          <V10Pager
            page={page}
            pageSize={PAGE_SIZE}
            total={total}
            onPageChange={setPage}
          />
        </>
      )}
    </div>
  )
}