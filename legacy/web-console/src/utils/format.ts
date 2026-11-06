/** Formatting helpers. All money in this product is micros: 1 元 = 1_000_000. */

export const MICROS_PER_YUAN = 1_000_000

const intFmt = new Intl.NumberFormat('zh-CN')

/** 1234567 -> "1,234,567" */
export function fmtInt(n: number | null | undefined): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '—'
  return intFmt.format(Math.round(n))
}

/** Compact Chinese magnitude for stat tiles: 1234000 -> "123.4万". */
export function fmtCompact(n: number | null | undefined): string {
  if (n === null || n === undefined || Number.isNaN(n)) return '—'
  const abs = Math.abs(n)
  if (abs >= 1e8) return `${(n / 1e8).toFixed(2)}亿`
  if (abs >= 1e4) return `${(n / 1e4).toFixed(2)}万`
  return intFmt.format(Math.round(n))
}

/** Token counts read better compacted once they pass 10k. */
export function fmtTokens(n: number | null | undefined): string {
  if (n === null || n === undefined) return '—'
  return Math.abs(n) >= 1e5 ? fmtCompact(n) : fmtInt(n)
}

/** micros -> 元 as a bare number string ("12.40"), no symbol. */
export function toYuan(micros: number | null | undefined, digits = 2): string {
  if (micros === null || micros === undefined || Number.isNaN(micros)) return '—'
  return (micros / MICROS_PER_YUAN).toFixed(digits)
}

/** micros -> "¥12.40". */
export function fmtMoney(micros: number | null | undefined, digits = 2): string {
  if (micros === null || micros === undefined || Number.isNaN(micros)) return '—'
  const v = micros / MICROS_PER_YUAN
  return `${v < 0 ? '-' : ''}¥${Math.abs(v).toFixed(digits)}`
}

/** micros -> "¥12.40" with thousands separators for large ledgers. */
export function fmtMoneyLong(micros: number | null | undefined, digits = 2): string {
  if (micros === null || micros === undefined || Number.isNaN(micros)) return '—'
  const v = micros / MICROS_PER_YUAN
  const [i, d] = Math.abs(v).toFixed(digits).split('.')
  return `${v < 0 ? '-' : ''}¥${intFmt.format(Number(i))}${d ? `.${d}` : ''}`
}

export function yuanToMicros(yuan: number | string): number {
  const n = typeof yuan === 'string' ? Number(yuan) : yuan
  if (!Number.isFinite(n)) return 0
  return Math.round(n * MICROS_PER_YUAN)
}

/** Ratio 0..1 -> "12.34%"; pass `ratio=false` for already-percent values. */
export function fmtPercent(v: number | null | undefined, digits = 2, ratio = true): string {
  if (v === null || v === undefined || Number.isNaN(v)) return '—'
  const p = ratio ? v * 100 : v
  return `${p.toFixed(digits)}%`
}

export function fmtMs(v: number | null | undefined): string {
  if (v === null || v === undefined) return '—'
  return v >= 1000 ? `${(v / 1000).toFixed(2)} s` : `${Math.round(v)} ms`
}

/* ---------------------------------------------------------------- */
/* Time: the API speaks unix seconds everywhere.                     */
/* ---------------------------------------------------------------- */

const pad = (n: number) => String(n).padStart(2, '0')

export function fmtDateTime(unix: number | null | undefined): string {
  if (!unix) return '—'
  const d = new Date(unix * 1000)
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

export function fmtDate(unix: number | null | undefined): string {
  if (!unix) return '—'
  const d = new Date(unix * 1000)
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

export function fmtDay(unix: number | null | undefined): string {
  return fmtDate(unix)
}

export function fmtRelative(unix: number | null | undefined): string {
  if (!unix) return '从未使用'
  const diff = Math.floor(Date.now() / 1000) - unix
  if (diff < 60) return '刚刚'
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`
  if (diff < 86400 * 30) return `${Math.floor(diff / 86400)} 天前`
  return fmtDate(unix)
}

/** Days remaining until a unix deadline; null deadline -> null. */
export function daysLeft(unix: number | null | undefined): number | null {
  if (!unix) return null
  return Math.max(0, Math.ceil((unix - Date.now() / 1000) / 86400))
}

export function todayStartUnix(): number {
  const d = new Date()
  d.setHours(0, 0, 0, 0)
  return Math.floor(d.getTime() / 1000)
}

export function shiftDays(unix: number, days: number): number {
  return unix + days * 86400
}

/* ---------------------------------------------------------------- */
/* Misc                                                              */
/* ---------------------------------------------------------------- */

export function ratio(part: number, total: number): number {
  if (!total || total <= 0) return 0
  return Math.min(1, Math.max(0, part / total))
}

export function clampPct(part: number, total: number): number {
  return Math.round(ratio(part, total) * 1000) / 10
}

/** Very small helper so a 0 value never renders as an empty cell. */
export function dash(v: string | number | null | undefined): string {
  if (v === null || v === undefined || v === '') return '—'
  return String(v)
}

export function downloadText(filename: string, content: string, mime = 'text/plain;charset=utf-8') {
  const blob = new Blob(['\uFEFF' + content], { type: mime })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    /* fall through to the legacy path */
  }
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    return ok
  } catch {
    return false
  }
}
