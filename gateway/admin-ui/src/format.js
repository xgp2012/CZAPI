// 时间与格式化工具。
//
// 后端返回的都是 Unix 秒，这里统一转成本地时间字符串。
// 不用 toLocaleString 的默认行为，避免不同浏览器格式不一致。
export function fmtTime(sec) {
  if (!sec || Number(sec) <= 0) return '—'
  const d = new Date(Number(sec) * 1000)
  const p = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

// quota → 元。perUnit 从后端取，前端不写死比例。
export function quotaToYuan(quota, perUnit) {
  const unit = Number(perUnit) || 500000
  return (Number(quota || 0) / unit).toFixed(2)
}

export function fmtNum(n) {
  return Number(n || 0).toLocaleString('zh-CN')
}
