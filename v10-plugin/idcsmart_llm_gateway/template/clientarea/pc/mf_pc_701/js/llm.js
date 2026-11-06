/*
 * LLM 网关 —— iframe 壳（pc/mf_pc_701 主题）
 *
 * 与 pc/default 逻辑一致；本主题根节点为 #app，布局组件为 common-layout。
 */
(function (window, undefined) {
  var old_onload = window.onload;
  window.onload = function () {
    const template = document.getElementById("app");
    Vue.prototype.lang = Object.assign(window.lang, window.plugin_lang || {});

    new Vue({
      components: {
        commonLayout,
        pagination,
      },
      data() {
        return {
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
                this.errorMsg = body.msg || lang.llm_ticket_failed;
                this.loading = false;
              }
            })
            .catch((err) => {
              this.errorMsg =
                (err && err.data && err.data.msg) || lang.llm_ticket_failed;
              this.loading = false;
            });
        },
        onFrameLoad() {
          this.loading = false;
        },
      },
    }).$mount(template);

    typeof old_onload == "function" && old_onload();
  };
})(window);
