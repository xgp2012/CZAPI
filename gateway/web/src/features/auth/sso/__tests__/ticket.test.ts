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
import { describe, expect, it } from 'vitest'

import { readTicketFromHash } from '../api'

describe('readTicketFromHash', () => {
  it('extracts a bare ticket from the fragment', () => {
    expect(readTicketFromHash('#abc123def456')).toBe('abc123def456')
  })

  it('accepts a hash without the leading marker', () => {
    expect(readTicketFromHash('abc123def456')).toBe('abc123def456')
  })

  it('extracts a ticket presented as a named parameter', () => {
    expect(readTicketFromHash('#ticket=abc123')).toBe('abc123')
  })

  it('prefers the named parameter when extra fragment fields are present', () => {
    expect(readTicketFromHash('#ticket=abc123&source=v10')).toBe('abc123')
  })

  it('returns an empty string for an empty fragment', () => {
    expect(readTicketFromHash('')).toBe('')
    expect(readTicketFromHash('#')).toBe('')
  })

  it('rejects a fragment that is not a bare ticket', () => {
    // 出现 `=` 说明不是我们签发的裸票据，宁可失败也不要把它当票据发出去。
    expect(readTicketFromHash('#foo=bar')).toBe('')
  })

  it('does not treat the fragment as a query string of unrelated keys', () => {
    expect(readTicketFromHash('#state=xyz')).toBe('')
  })
})
