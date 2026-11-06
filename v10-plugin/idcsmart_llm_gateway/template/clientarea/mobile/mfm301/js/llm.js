/*
 * LLM 网关 —— iframe 壳（mobile/mfm301 模板）
 *
 * ★ 本模板同样是 Vue3 + Vant（app.use(vant).mount("#template")），
 *   与 mfm201 一致；差异只在页面骨架（van-sticky 顶部条）。
 */
(function (window, undefined) {
  var old_onload = window.onload;
  window.onload = function () {
    typeof old_onload == "function" && old_onload();
    window.lang = Object.assign(window.lang, window.plugin_lang || {});

    const { showToast } = vant;

    const app = Vue.createApp({
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
        /* 申请一次性 SSO 票据并打开控制台 */
        loadTicket() {
          this.loading = true;
          this.errorMsg = "";

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

    if (window.directiveInfo && Array.isArray(window.directiveInfo)) {
      window.directiveInfo.forEach((item) => {
        app.directive(item.name, item.fn);
      });
    }

    app.use(vant).mount("#template");
  };
})(window);
