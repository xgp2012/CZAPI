// 检查 AGPL Section 7(b) 要求的署名是否真的会被用户看到。
// 只看语言包里有键是不够的：组件必须引用它。
const fs = require('fs')
const path = require('path')

const dir = 'dist/static/js'
const needle = 'Frontend design and development by New API contributors'
const files = []
;(function walk(d) {
  for (const e of fs.readdirSync(d, { withFileTypes: true })) {
    const p = path.join(d, e.name)
    if (e.isDirectory()) walk(p)
    else if (e.name.endsWith('.js')) files.push(p)
  }
})(dir)

let localeCopies = 0
let rendered = 0
for (const f of files) {
  const src = fs.readFileSync(f, 'utf8')
  let i = 0
  while ((i = src.indexOf(needle, i)) !== -1) {
    // 语言包里是 "键":"值" 紧邻形式
    const isLocaleEntry = src.slice(i + needle.length, i + needle.length + 4).startsWith('":"')
    if (isLocaleEntry) localeCopies++
    else {
      rendered++
      console.log('RENDERED USAGE in', path.basename(f))
      console.log('  context:', JSON.stringify(src.slice(Math.max(0, i - 70), i + 50)))
    }
    i += needle.length
  }
}

const hasFn = files.some((f) => fs.readFileSync(f, 'utf8').includes('ProjectAttribution'))
console.log('---')
console.log('locale entries :', localeCopies, '(期望 7)')
console.log('rendered usages:', rendered, '(期望 >=1)')
console.log('footer component bundled:', hasFn)
