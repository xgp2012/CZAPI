/*
 * 语言包动态引入（与官方 idcsmart_ticket 的 template/admin/js/lang.js 完全一致）
 *
 * 后台当前语言存在 localStorage.backLang（zh-cn / zh-hk / en-us）。
 * document.writeln 在解析期同步插入 <script>，保证后续 js 运行时 window.plugin_lang 已就绪。
 */
(function () {
  if (localStorage.getItem('backLang') == null) {
    document.writeln('<script src="/plugins/addon/idcsmart_llm_gateway/template/admin/lang/zh-cn.js"><\/script>')
  } else {
    document.writeln('<script src="/plugins/addon/idcsmart_llm_gateway/template/admin/lang/' + localStorage.getItem('backLang') + '.js"><\/script>')
  }
}())
