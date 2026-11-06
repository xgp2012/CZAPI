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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { handleServerError } from '@/lib/handle-server-error'

import { getHosts, setHostStatus } from '../api'
import type { V10Host } from '../types'
import { V10FilterBar, V10Pager } from './filter-bar'
import { formatUnixDateTime } from '../lib/format-time'

const PAGE_SIZE = 20

const HOST_STATUS_VARIANT: Record<string, 'default' | 'secondary' | 'destructive' | 'outline'> = {
  active: 'default',
  suspended: 'secondary',
  terminated: 'destructive',
}

/**
 * Host 与订阅。
 *
 * host 是 V10 的一个"产品实例"，对应网关上的一条订阅。暂停与吊销必须分开：
 * 暂停（欠费）可恢复，吊销（产品删除）是永久的——早期实现把两者都置成
 * disabled，导致"恢复套餐"把客户早就作废的 Key 全部复活。
 */
export function V10Hosts() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [page, setPage] = useState(1)
  const [keyword, setKeyword] = useState('')
  const [status, setStatus] = useState('')

  const query = useQuery({
    queryKey: ['v10', 'hosts', page, keyword, status],
    queryFn: () =>
      getHosts({
        page,
        page_size: PAGE_SIZE,
        keyword,
        status: status || undefined,
      }),
    retry: false,
  })

  const statusMutation = useMutation({
    mutationFn: (payload: { id: number; status: string }) =>
      setHostStatus(payload.id, payload.status),
    onSuccess: () => {
      toast.success(t('Status updated'))
      void queryClient.invalidateQueries({ queryKey: ['v10', 'hosts'] })
    },
    onError: (error) => handleServerError(error),
  })

  if (query.isLoading) return <LoadingState />
  if (query.isError) return <ErrorState onRetry={query.refetch} />

  const items = query.data?.items ?? []
  const total = query.data?.total ?? 0

  const statusOptions = [
    { value: 'active', label: t('Active') },
    { value: 'suspended', label: t('Suspended') },
    { value: 'terminated', label: t('Terminated') },
  ]

  return (
    <div className='space-y-3'>
      <V10FilterBar
        keyword={keyword}
        onKeywordChange={setKeyword}
        onSearch={() => setPage(1)}
        placeholder={t('Search by host ID or client ID')}
        filter={{
          value: status,
          onChange: (value) => {
            setStatus(value)
            setPage(1)
          },
          placeholder: t('All statuses'),
          options: statusOptions,
        }}
      />

      {items.length === 0 ? (
        <EmptyState
          bordered
          title={t('No subscriptions yet')}
          description={t(
            'Hosts appear here when V10 provisions a plan via /internal/v10/hosts.'
          )}
        />
      ) : (
        <>
          <StaticDataTable<V10Host>
            data={items}
            getRowKey={(row) => row.id}
            columns={[
              {
                id: 'host_id',
                header: t('Host ID'),
                className: 'w-24',
                cell: (row) => (
                  <span className='font-mono text-xs'>{row.host_id}</span>
                ),
              },
              {
                id: 'client',
                header: t('Client'),
                className: 'w-24',
                cell: (row) => (
                  <span className='font-mono text-xs'>{row.client_id}</span>
                ),
              },
              {
                id: 'user',
                header: t('User ID'),
                className: 'w-20',
                cell: (row) => row.user_id,
              },
              {
                id: 'plan',
                header: t('Plan'),
                className: 'w-24',
                cell: (row) => row.plan_id || '-',
              },
              {
                id: 'status',
                header: t('Status'),
                className: 'w-28',
                cell: (row) => (
                  <Badge variant={HOST_STATUS_VARIANT[row.host_status] ?? 'outline'}>
                    {row.host_status || '-'}
                  </Badge>
                ),
              },
              {
                id: 'created_at',
                header: t('Created'),
                className: 'w-44',
                cell: (row) => (
                  <span className='text-muted-foreground text-xs'>
                    {formatUnixDateTime(row.created_at)}
                  </span>
                ),
              },
              {
                id: 'actions',
                header: '',
                className: 'w-44',
                cell: (row) => {
                  const isActive = row.host_status === 'active'
                  return (
                    <div className='flex justify-end'>
                      <Button
                        size='sm'
                        variant={isActive ? 'secondary' : 'outline'}
                        disabled={statusMutation.isPending}
                        onClick={() => {
                          const next = isActive ? 'suspended' : 'active'
                          // 后端 POST /hosts/:id 按 v10_hosts 主键定位，
                          // 不是 V10 侧的 host_id，两者不能混用。
                          statusMutation.mutate({
                            id: row.id,
                            status: next,
                          })
                        }}
                      >
                        {isActive ? t('Suspend') : t('Resume')}
                      </Button>
                    </div>
                  )
                },
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