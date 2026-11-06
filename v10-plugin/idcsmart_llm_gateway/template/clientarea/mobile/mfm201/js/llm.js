/*
 * LLM 网关 —— iframe 壳（mobile/mfm201 模板）
 *
 * ★ 本模板是 Vue3 + Vant，挂载方式与 PC 主题不同（源码实证）：
 *     const app = Vue.createApp({...});
 *     window.directiveInfo.forEach(item => app.directive(item.name, item.fn));
 *     app.use(vant).mount("#template");
 *   PC 主题才是 Vue2 + new Vue({...}).$mount(...)。
 *
 * 业务逻辑与 PC 一致：调 console/v1/llm/sso-ticket 换 sso_url -> 赋值给 iframe.src -> onload 关 loading。
 */
(function (window, undefined) {
  var old_onload = window.onload;
  window.onload = function () {
    typeof old_onload == "function" && old_onload();
    window.lang = Object.assign(window.lang, window.plugin_lang || {});

    const { showToast } = vant;

    const app = Vue.createApp({
      components: {
        topMenu,
      },
      data() {
        return {
          lang: window.lang,
          ssoUrl: "",
          loading: true,
          errorMsg: "",
        };
      },
      created() {
        this.loadTicket();
      },
      methods: {
        /* 返回上一页（与官方移动端模板一致的导航行为） */
        goBack() {
          history.back();
        },
        /* 申请一次性 SSO 票据并打开控制台 */
        loadTicket() {
          this.loading = true;
          this.errorMsg = "";

          /* Axios 为 V10 前台全局实例，baseURL 指向 /console/v1，自带 JWT 头 */
          Axios.get("/llm/sso-ticket")
            .then((res) => {
              const body = res.data || {};
              if (body.status === 200 && body.data && body.data.sso_url) {
                this.ssoUrl = body.data.sso_url;
              } else {
                this.errorMsg = body.msg || window.lang.llm_ticket_failed;
                this.loading = false;
                showToast(this.errorMsg);
              }
            })
            .catch((err) => {
              this.errorMsg =
                (err && err.data && err.data.msg) || window.lang.llm_ticket_failed;
              this.loading = false;
              showToast(this.errorMsg);
            });
        },
        /* iframe 加载完成 -> 关闭 loading */
        onFrameLoad() {
          this.loading = false;
        },
      },
    });

    /* 系统注入的全局指令（官方模板每个页面都会注册，缺了会丢权限/格式化指令） */
    if (window.directiveInfo && Array.isArray(window.directiveInfo)) {
      window.directiveInfo.forEach((item) => {
        app.directive(item.name, item.fn);
      });
    }

    app.use(vant).mount("#template");
  };
})(window);
