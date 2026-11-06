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
import { Plus, RefreshCw, RotateCw, ShieldAlert } from 'lucide-react'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { CopyButton } from '@/components/copy-button'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table'
import { EmptyState } from '@/components/empty-state'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Badge } from '@/components/ui/badge'
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
import { handleServerError } from '@/lib/handle-server-error'

import { createSite, getSites, rotateSiteSecret, setSiteStatus } from '../api'
import { formatUnixDateTime } from '../lib/format-time'
import type { V10Site, V10SiteSecret } from '../types'

type CreateForm = { app_id: string; name: string }

/**
 * V10 站点管理。
 *
 * 站点 = 一个 V10 业务系统。app_id/secret 是网关与它之间的唯一凭据。
 * 明文 secret 只在创建与轮换的响应里出现一次，库里只存哈希与前缀。
 */
export function V10Sites() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [createOpen, createHandlers] = useDialog()
  const [secretOpen, secretHandlers] = useDialog()
  const [rotateTarget, setRotateTarget] = useState<V10Site | null>(null)
  const [newSecret, setNewSecret] = useState<V10SiteSecret | null>(null)

  const query = useQuery({
    queryKey: ['v10', 'sites'],
    queryFn: getSites,
    retry: false,
  })

  const form = useForm<CreateForm>({
    defaultValues: { app_id: '', name: '' },
  })

  const createMutation = useMutation({
    mutationFn: createSite,
    onSuccess: (data) => {
      createHandlers.close()
      form.reset()
      setNewSecret(data)
      secretHandlers.open()
      void queryClient.invalidateQueries({ queryKey: ['v10', 'sites'] })
    },
    onError: (error) => handleServerError(error),
  })

  const rotateMutation = useMutation({
    mutationFn: rotateSiteSecret,
    onSuccess: (data) => {
      setRotateTarget(null)
      setNewSecret(data)
      secretHandlers.open()
      void queryClient.invalidateQueries({ queryKey: ['v10', 'sites'] })
    },
    onError: (error) => handleServerError(error),
  })

  const statusMutation = useMutation({
    mutationFn: (payload: { id: number; enabled: boolean }) =>
      setSiteStatus(payload.id, payload.enabled),
    onSuccess: () => {
      toast.success(t('Updated'))
      void queryClient.invalidateQueries({ queryKey: ['v10', 'sites'] })
    },
    onError: (error) => handleServerError(error),
  })

  if (query.isLoading) return <LoadingState />
  if (query.isError) return <ErrorState onRetry={query.refetch} />

  const items = query.data?.items ?? []

  return (
    <div className='space-y-4'>
      <div className='flex flex-wrap items-center gap-2'>
        <Button size='sm' onClick={createHandlers.open}>
          <Plus className='size-4' />
          {t('New Site')}
        </Button>
        <Button
          size='sm'
          variant='outline'
          onClick={() => void query.refetch()}
          disabled={query.isFetching}
        >
          <RefreshCw
            className={query.isFetching ? 'size-4 animate-spin' : 'size-4'}
          />
          {t('Refresh')}
        </Button>
      </div>

      {items.length === 0 ? (
        <EmptyState
          bordered
          title={t('No sites yet')}
          description={t(
            'Create a site, then copy its app_id and secret into the V10 plugin configuration.'
          )}
          action={
            <Button size='sm' onClick={createHandlers.open}>
              {t('New Site')}
            </Button>
          }
        />
      ) : (
        <StaticDataTable<V10Site>
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
              id: 'app_id',
              header: t('App ID'),
              cell: (row) => (
                <span className='font-mono text-xs'>{row.app_id}</span>
              ),
            },
            { id: 'name', header: t('Name'), cell: (row) => row.name },
            {
              id: 'secret',
              header: t('Secret prefix'),
              cell: (row) => (
                <span className='text-muted-foreground font-mono text-xs'>
                  {row.secret_prefix}…
                </span>
              ),
            },
            {
              id: 'status',
              header: t('Status'),
              className: 'w-24',
              cell: (row) => (
                <Badge variant={row.status === 1 ? 'default' : 'secondary'}>
                  {row.status === 1 ? t('Enabled') : t('Disabled')}
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
              className: 'w-60',
              cell: (row) => (
                <div className='flex justify-end gap-2'>
                  <Button
                    size='sm'
                    variant='outline'
                    disabled={rotateMutation.isPending}
                    onClick={() => setRotateTarget(row)}
                  >
                    <RotateCw className='size-4' />
                    {t('Rotate secret')}
                  </Button>
                  <Button
                    size='sm'
                    variant={row.status === 1 ? 'destructive' : 'default'}
                    disabled={statusMutation.isPending}
                    onClick={() =>
                      statusMutation.mutate({
                        id: row.id,
                        enabled: row.status !== 1,
                      })
                    }
                  >
                    {row.status === 1 ? t('Disable') : t('Enable')}
                  </Button>
                </div>
              ),
            },
          ]}
        />
      )}

      <ConfirmDialog
        open={rotateTarget !== null}
        onOpenChange={(open) => {
          if (!open) setRotateTarget(null)
        }}
        title={t('Rotate the site secret?')}
        desc={t(
          'The old secret stops working immediately. The V10 plugin must be updated with the new one, otherwise every request fails with 401.'
        )}
        confirmText={t('Rotate secret')}
        destructive
        isLoading={rotateMutation.isPending}
        handleConfirm={() => {
          if (rotateTarget) rotateMutation.mutate(rotateTarget.id)
        }}
      />

      <Dialog
        open={createOpen}
        onOpenChange={(open) =>
          open ? createHandlers.open() : createHandlers.close()
        }
      >
        <DialogContent className='sm:max-w-md'>
          <DialogHeader>
            <DialogTitle>{t('New Site')}</DialogTitle>
            <DialogDescription>
              {t(
                'A site represents one V10 business system. The secret is generated by the gateway.'
              )}
            </DialogDescription>
          </DialogHeader>

          <Form {...form}>
            <form
              className='space-y-4'
              onSubmit={form.handleSubmit((values) =>
                createMutation.mutate({
                  app_id: values.app_id.trim(),
                  name: values.name.trim() || undefined,
                })
              )}
            >
              <FormField
                control={form.control}
                name='app_id'
                rules={{ required: t('App ID is required') }}
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('App ID')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder='czapi'
                        autoComplete='off'
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      {t('Used as the X-V10-App-Id header value.')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='name'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Display name')}</FormLabel>
                    <FormControl>
                      <Input
                        placeholder={t('Defaults to the App ID')}
                        autoComplete='off'
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
                  onClick={createHandlers.close}
                >
                  {t('Cancel')}
                </Button>
                <Button type='submit' disabled={createMutation.isPending}>
                  {t('Create')}
                </Button>
              </DialogFooter>
            </form>
          </Form>
        </DialogContent>
      </Dialog>

      <Dialog
        open={secretOpen}
        onOpenChange={(open) =>
          open ? secretHandlers.open() : secretHandlers.close()
        }
      >
        <DialogContent className='sm:max-w-lg'>
          <DialogHeader>
            <DialogTitle>{t('Save this secret now')}</DialogTitle>
            <DialogDescription>
              {t(
                'This is the only time the plaintext secret is shown. The gateway stores only a hash.'
              )}
            </DialogDescription>
          </DialogHeader>

          <div className='bg-destructive/10 text-destructive flex items-start gap-2 rounded-md p-3 text-sm'>
            <ShieldAlert className='mt-0.5 size-4 shrink-0' />
            <span>
              {newSecret?.warning ??
                newSecret?.notice ??
                t('Store it in a password manager before closing this dialog.')}
            </span>
          </div>

          <div className='bg-muted flex items-center gap-2 rounded-md p-3'>
            <code className='min-w-0 flex-1 font-mono text-xs break-all'>
              {newSecret?.secret ?? ''}
            </code>
            <CopyButton value={newSecret?.secret ?? ''} />
          </div>

          <DialogFooter>
            <Button onClick={secretHandlers.close}>{t('I have saved it')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
