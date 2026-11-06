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
import dayjs from '@/lib/dayjs'

/**
 * Format a Unix timestamp (seconds) for the V10 operations tables.
 *
 * Returns `-` for 0/absent values on purpose: the V10 tables use 0 as
 * "never", and rendering 1970-01-01 there would read as a real event.
 */
export function formatUnixDateTime(tsSec: number | null | undefined): string {
  if (!tsSec || tsSec <= 0) return '-'
  return dayjs(tsSec * 1000).format('YYYY-MM-DD HH:mm:ss')
}

/** Date-only variant, for day-bucketed usage rows. */
export function formatUnixDate(tsSec: number | null | undefined): string {
  if (!tsSec || tsSec <= 0) return '-'
  return dayjs(tsSec * 1000).format('YYYY-MM-DD')
}
