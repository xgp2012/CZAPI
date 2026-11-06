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
import { Search } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

type V10FilterBarProps = {
  keyword: string
  onKeywordChange: (value: string) => void
  onSearch: () => void
  placeholder?: string
  /** 右侧下拉筛选，留空则不渲染。 */
  filter?: {
    value: string
    onChange: (value: string) => void
    placeholder: string
    options: { value: string; label: string }[]
  }
  /** 右侧额外按钮，例如刷新。 */
  actions?: React.ReactNode
}

/**
 * V10 运营表的统一筛选栏：关键字 + 可选下拉 + 动作。
 *
 * 抽出来是为了让六个页面看起来是同一个产品的六个页签，而不是六个页面拼接。
 */
export function V10FilterBar(props: V10FilterBarProps) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState(props.keyword)

  return (
    <div className='flex flex-wrap items-center gap-2'>
      <div className='relative w-full sm:w-64'>
        <Search className='text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2' />
        <Input
          className='pl-8'
          value={draft}
          placeholder={props.placeholder ?? t('Search')}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={(event) => {
            if (event.key !== 'Enter') return
            props.onKeywordChange(draft.trim())
            props.onSearch()
          }}
        />
      </div>

      <Button
        size='sm'
        variant='secondary'
        onClick={() => {
          props.onKeywordChange(draft.trim())
          props.onSearch()
        }}
      >
        {t('Search')}
      </Button>

      {props.filter != null && (
        <Select
          value={props.filter.value || 'all'}
          onValueChange={(value) => {
            if (value === null) return
            props.filter?.onChange(value === 'all' ? '' : value)
          }}
        >
          <SelectTrigger className='w-40'>
            <SelectValue placeholder={props.filter.placeholder} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value='all'>{props.filter.placeholder}</SelectItem>
            {props.filter.options.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}

      {props.actions}
    </div>
  )
}

/** 统一的上一页/下一页条。后端不返回总页数，用总数自行算。 */
export function V10Pager(props: {
  page: number
  pageSize: number
  total: number
  onPageChange: (page: number) => void
}) {
  const { t } = useTranslation()
  const lastPage = Math.max(1, Math.ceil(props.total / props.pageSize))

  return (
    <div className='flex flex-wrap items-center justify-between gap-2 pt-1'>
      <span className='text-muted-foreground text-xs'>
        {t('Total')}: {props.total} · {t('Page')} {props.page} / {lastPage}
      </span>
      <div className='flex gap-2'>
        <Button
          size='sm'
          variant='outline'
          disabled={props.page <= 1}
          onClick={() => props.onPageChange(props.page - 1)}
        >
          {t('Previous')}
        </Button>
        <Button
          size='sm'
          variant='outline'
          disabled={props.page >= lastPage}
          onClick={() => props.onPageChange(props.page + 1)}
        >
          {t('Next')}
        </Button>
      </div>
    </div>
  )
}