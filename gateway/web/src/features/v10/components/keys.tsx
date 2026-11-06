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
import { Ban } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { formatQuota } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'

import { getKeys, revokeKey } from '../api'
import type { V10Key } from '../types'
import { V10FilterBar, V10Pager } from './filter-bar'
import { formatUnixDateTime } from '../lib/format-time'

const PAGE_SIZE = 20

const TOKEN_STATUS_ENABLED = 1
const TOKEN_STATUS_DISABLED = 2

/**
 * Key 管理（只读 + 封禁）。
 *
 * Key 的创建、续期都由 V10 调 /internal/v10/keys 完成；这里只做检索与封禁，
 * 因为"客户在 V10 那边点删除"和"运维紧急止血"是两种不同的事，
 * 后者需要一个不走 V10 的断路器。
 */
export function V10Keys() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [page, setPage] = useState(1)
  const [keyword, setKeyword] = useState('')
  const [revokeTarget, setRevokeTarget] = useState<V10Key | null>(null)

  const query = useQuery({
    queryKey: ['v10', 'keys', page, keyword],
    queryFn: () => getKeys({ page, page_size: PAGE_SIZE, keyword }),
    retry: false,
  })

  const revokeMutation = useMutation({
    mutationFn: revokeKey,
    onSuccess: () => {
      setRevokeTarget(null)
      toast.success(t('Key revoked'))
      void queryClient.invalidateQueries({ queryKey: ['v10', 'keys'] })
    },
    onError: (error) => handleServerError(error),
  })

  if (query.isLoading) return <LoadingState />
  if (query.isError) return <ErrorState onRetry={query.refetch} />

  const items = query.data?.items ?? []
  const total = query.data?.total ?? 0

  return (
    <div className='space-y-3'>
      <V10FilterBar
        keyword={keyword}
        onKeywordChange={setKeyword}
        onSearch={() => setPage(1)}
        placeholder={t('Search by key name')}
      />

      {items.length === 0 ? (
        <EmptyState
          bordered
          title={t('No keys yet')}
          description={t(
            'Keys are created by V10 when a subscription is provisioned.'
          )}
        />
      ) : (
        <>
          <StaticDataTable<V10Key>
            data={items}
            getRowKey={(row) => row.id}
            columns={[
              {
                id: 'name',
                header: t('Name'),
                cell: (row) => (
                  <span className='font-mono text-xs'>{row.name}</span>
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
                header: t('Username'),
                cell: (row) => (
                  <span className='text-muted-foreground font-mono text-xs'>
                    {row.username}
                  </span>
                ),
              },
              {
                id: 'status',
                header: t('Status'),
                className: 'w-24',
                cell: (row) => (
                  <Badge
                    variant={
                      row.status === TOKEN_STATUS_ENABLED
                        ? 'default'
                        : 'secondary'
                    }
                  >
                    {row.status === TOKEN_STATUS_ENABLED
                      ? t('Enabled')
                      : t('Disabled')}
                  </Badge>
                ),
              },
              {
                id: 'used',
                header: t('Used'),
                className: 'w-32',
                cell: (row) =>
                  row.unlimited_quota ? (
                    <span className='text-muted-foreground text-xs'>
                      {t('Unlimited')}
                    </span>
                  ) : (
                    <span className='tabular-nums'>
                      {formatQuota(row.used_quota)}
                    </span>
                  ),
              },
              {
                id: 'expires',
                header: t('Expires'),
                className: 'w-44',
                cell: (row) => (
                  <span className='text-muted-foreground text-xs'>
                    {formatUnixDateTime(row.expired_time)}
                  </span>
                ),
              },
              {
                id: 'actions',
                header: '',
                className: 'w-28',
                cell: (row) => (
                  <div className='flex justify-end'>
                    <Button
                      size='sm'
                      variant='destructive'
                      disabled={
                        row.status === TOKEN_STATUS_DISABLED ||
                        revokeMutation.isPending
                      }
                      onClick={() => setRevokeTarget(row)}
                    >
                      <Ban className='size-4' />
                      {t('Revoke')}
                    </Button>
                  </div>
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
    <ConfirmDialog
        open={revokeTarget !== null}
        onOpenChange={(open) => {
          if (!open) setRevokeTarget(null)
        }}
        title={t('Revoke this key?')}
        desc={t(
          'Revocation is permanent for the client. V10 must issue a new key before the subscription can call the gateway again.'
        )}
        confirmText={t('Revoke')}
        destructive
        isLoading={revokeMutation.isPending}
        handleConfirm={() => {
          if (revokeTarget) revokeMutation.mutate(revokeTarget.id)
        }}
      />
    </div>
  )
}