/*
 * LLM 网关 —— 前台（会员中心）语言包
 *
 * 机制（源码实证，见官方 idcsmart_ticket 的 template/clientarea/pc/default/lang/index.js）：
 *   - 本文件定义三套语言，按 localStorage.lang 选一套挂到 window.plugin_lang
 *   - 系统语言包挂在 window.lang，JS 里用 Object.assign(window.lang, window.plugin_lang) 合并
 *   - 结尾调用系统提供的 checkLangFun(plugin_lang) 做缺键检查（若系统未提供则跳过）
 */
(function () {
  const plugin_lang = {
    "zh-cn": {
      llm_title: "LLM 网关",
      llm_desc: "在此管理你的 API Key、查看模型与用量。控制台由网关提供，通过一次性票据安全登录。",
      llm_loading: "正在进入控制台…",
      llm_retry: "重新加载",
      llm_ticket_failed: "获取登录票据失败，请稍后重试",
      llm_frame_hint: "若长时间无响应，请刷新页面或联系客服",
    },
    "zh-hk": {
      llm_title: "LLM 網關",
      llm_desc: "在此管理你的 API Key、檢視模型與用量。控制台由網關提供，透過一次性票據安全登入。",
      llm_loading: "正在進入控制台…",
      llm_retry: "重新載入",
      llm_ticket_failed: "取得登入票據失敗，請稍後重試",
      llm_frame_hint: "若長時間無回應，請重新整理頁面或聯絡客服",
    },
    "en-us": {
      llm_title: "LLM Gateway",
      llm_desc: "Manage your API keys, models and usage here. The console is served by the gateway and signed in with a one-time ticket.",
      llm_loading: "Opening the console…",
      llm_retry: "Reload",
      llm_ticket_failed: "Failed to obtain the sign-in ticket, please retry later",
      llm_frame_hint: "If nothing happens for a while, reload the page or contact support",
    },
  };

  const DEFAULT_LANG = localStorage.getItem("lang") || "zh-cn";
  window.plugin_lang = plugin_lang[DEFAULT_LANG] || plugin_lang["zh-cn"];

  /* checkLangFun 由系统语言包提供（用于缺键告警）；插件页面单独打开时可能不存在 */
  if (typeof checkLangFun === "function") {
    checkLangFun(plugin_lang);
  }
})();
