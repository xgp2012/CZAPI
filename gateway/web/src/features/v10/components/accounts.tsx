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
import { Coins } from 'lucide-react'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { useDialog } from '@/hooks/use-dialog'
import { formatQuota } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'

import { getAccounts, manualCredit } from '../api'
import type { V10Account } from '../types'
import { V10FilterBar, V10Pager } from './filter-bar'
import { formatUnixDateTime } from '../lib/format-time'

type CreditForm = { amount_yuan: string; remark: string }

const PAGE_SIZE = 20

/**
 * 账户与额度。
 *
 * 这里能看到的是"由 V10 建立"的账户（v10_accounts 有记录），
 * 运维自己建的 new-api 账户不在其中——这是有意的边界，避免误调客户的额度。
 */
export function V10Accounts() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [page, setPage] = useState(1)
  const [keyword, setKeyword] = useState('')
  const [target, setTarget] = useState<V10Account | null>(null)
  const [creditOpen, creditHandlers] = useDialog()

  const query = useQuery({
    queryKey: ['v10', 'accounts', page, keyword],
    queryFn: () => getAccounts({ page, page_size: PAGE_SIZE, keyword }),
    retry: false,
  })

  const form = useForm<CreditForm>({
    defaultValues: { amount_yuan: '', remark: '' },
  })

  const creditMutation = useMutation({
    mutationFn: (payload: CreditForm) =>
      manualCredit(target?.user_id ?? 0, {
        amount_yuan: Number(payload.amount_yuan),
        remark: payload.remark.trim() || undefined,
      }),
    onSuccess: () => {
      toast.success(t('Balance adjusted'))
      creditHandlers.close()
      form.reset()
      void queryClient.invalidateQueries({ queryKey: ['v10', 'accounts'] })
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
        placeholder={t('Search by client ID, user ID or username')}
      />

      {items.length === 0 ? (
        <EmptyState
          bordered
          title={t('No accounts yet')}
          description={t(
            'Accounts appear here once V10 calls /internal/v10/accounts/sync.'
          )}
        />
      ) : (
        <>
          <StaticDataTable<V10Account>
            data={items}
            getRowKey={(row) => row.user_id}
            columns={[
              {
                id: 'user_id',
                header: t('User ID'),
                className: 'w-20',
                cell: (row) => row.user_id,
              },
              {
                id: 'client_id',
                header: t('Client ID'),
                className: 'w-24',
                cell: (row) => (
                  <span className='font-mono text-xs'>{row.client_id}</span>
                ),
              },
              {
                id: 'username',
                header: t('Username'),
                cell: (row) => (
                  <span className='font-mono text-xs'>{row.username}</span>
                ),
              },
              {
                id: 'quota',
                header: t('Balance'),
                className: 'w-32',
                cell: (row) => (
                  <span className='tabular-nums'>{formatQuota(row.quota)}</span>
                ),
              },
              {
                id: 'used',
                header: t('Used'),
                className: 'w-32',
                cell: (row) => (
                  <span className='text-muted-foreground tabular-nums'>
                    {formatQuota(row.used_quota)}
                  </span>
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
                className: 'w-32',
                cell: (row) => (
                  <div className='flex justify-end'>
                    <Button
                      size='sm'
                      variant='outline'
                      onClick={() => {
                        setTarget(row)
                        form.reset()
                        creditHandlers.open()
                      }}
                    >
                      <Coins className='size-4' />
                      {t('Adjust')}
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

      <Dialog
        open={creditOpen}
        onOpenChange={(open) =>
          open ? creditHandlers.open() : creditHandlers.close()
        }
      >
        <DialogContent className='sm:max-w-md'>
          <DialogHeader>
            <DialogTitle>{t('Adjust balance')}</DialogTitle>
            <DialogDescription>
              {t(
                'Positive adds credit, negative deducts it. Every adjustment gets its own idempotency key, so it can be replayed safely.'
              )}
            </DialogDescription>
          </DialogHeader>

          <Form {...form}>
            <form
              className='space-y-4'
              onSubmit={form.handleSubmit((values) =>
                creditMutation.mutate(values)
              )}
            >
              <div className='text-muted-foreground text-sm'>
                {t('Account')}:{' '}
                <span className='text-foreground font-mono'>
                  {target?.username} (#{target?.user_id})
                </span>
              </div>

              <FormField
                control={form.control}
                name='amount_yuan'
                rules={{
                  required: t('Amount is required'),
                  validate: (value) =>
                    Number.isFinite(Number(value)) && Number(value) !== 0
                      ? true
                      : t('Amount must be a non-zero number'),
                }}
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Amount (CNY)')}</FormLabel>
                    <FormControl>
                      <Input
                        type='number'
                        step='0.01'
                        placeholder='100'
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t(
                        'Single-credit ceiling is about 4294.97 CNY; split larger amounts.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='remark'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Remark')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t('Reason for the adjustment')}
                        {...field}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <DialogFooter>
                <Button
                  type='button'
                  variant='outline'
                  onClick={creditHandlers.close}
                >
                  {t('Cancel')}
                </Button>
                <Button type='submit' disabled={creditMutation.isPending}>
                  {t('Confirm')}
                </Button>
              </DialogFooter>
            </form>
          </Form>
        </DialogContent>
      </Dialog>
    </div>
  )
}