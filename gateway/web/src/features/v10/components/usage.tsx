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

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { formatNumber, formatQuota } from '@/lib/format'

import { getUsageModels, getUsageSummary } from '../api'
import type { V10UsageModelRow, V10UsageRow } from '../types'

/**
 * 用量报表。
 *
 * 两个视角：按客户（谁的额度在烧）与按模型（哪个模型最贵）。
 * 聚合在 Go 侧完成，因此这里不需要关心底层是 SQLite、MySQL 还是 PostgreSQL。
 */
export function V10Usage() {
  const { t } = useTranslation()

  const summaryQuery = useQuery({
    queryKey: ['v10', 'usage', 'summary'],
    queryFn: () => getUsageSummary(),
    retry: false,
  })

  const modelsQuery = useQuery({
    queryKey: ['v10', 'usage', 'models'],
    queryFn: () => getUsageModels(),
    retry: false,
  })

  if (summaryQuery.isLoading || modelsQuery.isLoading) return <LoadingState />
  if (summaryQuery.isError || modelsQuery.isError) {
    return (
      <ErrorState
        onRetry={() => {
          void summaryQuery.refetch()
          void modelsQuery.refetch()
        }}
      />
    )
  }

  const byClient = summaryQuery.data?.items ?? []
  const byModel = modelsQuery.data?.items ?? []

  return (
    <Tabs defaultValue='clients'>
      <TabsList>
        <TabsTrigger value='clients'>{t('By client')}</TabsTrigger>
        <TabsTrigger value='models'>{t('By model')}</TabsTrigger>
      </TabsList>

      <TabsContent value='clients' className='pt-3'>
        {byClient.length === 0 ? (
          <EmptyState
            bordered
            title={t('No usage in this range')}
            description={t('Consumption logs drive this report.')}
          />
        ) : (
          <StaticDataTable<V10UsageRow>
            data={byClient}
            getRowKey={(row) => row.user_id}
            columns={[
              {
                id: 'client',
                header: t('Client ID'),
                className: 'w-24',
                cell: (row) => (
                  <span className='font-mono text-xs'>{row.client_id || '-'}</span>
                ),
              },
              {
                id: 'user',
                header: t('Username'),
                cell: (row) => (
                  <span className='font-mono text-xs'>{row.username || '-'}</span>
                ),
              },
              {
                id: 'requests',
                header: t('Requests'),
                className: 'w-24',
                cell: (row) => (
                  <span className='tabular-nums'>
                    {formatNumber(row.request_count)}
                  </span>
                ),
              },
              {
                id: 'prompt',
                header: t('Prompt tokens'),
                className: 'w-32',
                cell: (row) => (
                  <span className='text-muted-foreground tabular-nums'>
                    {formatNumber(row.prompt_tokens)}
                  </span>
                ),
              },
              {
                id: 'completion',
                header: t('Completion tokens'),
                className: 'w-36',
                cell: (row) => (
                  <span className='text-muted-foreground tabular-nums'>
                    {formatNumber(row.completion_tokens)}
                  </span>
                ),
              },
              {
                id: 'quota',
                header: t('Cost'),
                className: 'w-32',
                cell: (row) => (
                  <span className='tabular-nums'>{formatQuota(row.quota)}</span>
                ),
              },
            ]}
          />
        )}
      </TabsContent>

      <TabsContent value='models' className='pt-3'>
        {byModel.length === 0 ? (
          <EmptyState
            bordered
            title={t('No usage in this range')}
            description={t('Consumption logs drive this report.')}
          />
        ) : (
          <StaticDataTable<V10UsageModelRow>
            data={byModel}
            getRowKey={(row) => row.model_name}
            columns={[
              {
                id: 'model',
                header: t('Model'),
                cell: (row) => (
                  <span className='font-mono text-xs'>{row.model_name || '-'}</span>
                ),
              },
              {
                id: 'requests',
                header: t('Requests'),
                className: 'w-24',
                cell: (row) => (
                  <span className='tabular-nums'>
                    {formatNumber(row.request_count)}
                  </span>
                ),
              },
              {
                id: 'prompt',
                header: t('Prompt tokens'),
                className: 'w-32',
                cell: (row) => (
                  <span className='text-muted-foreground tabular-nums'>
                    {formatNumber(row.prompt_tokens)}
                  </span>
                ),
              },
              {
                id: 'completion',
                header: t('Completion tokens'),
                className: 'w-36',
                cell: (row) => (
                  <span className='text-muted-foreground tabular-nums'>
                    {formatNumber(row.completion_tokens)}
                  </span>
                ),
              },
              {
                id: 'quota',
                header: t('Cost'),
                className: 'w-32',
                cell: (row) => (
                  <span className='tabular-nums'>{formatQuota(row.quota)}</span>
                ),
              },
            ]}
          />
        )}
      </TabsContent>
    </Tabs>
  )
}