/*
 * LLM 网关 —— iframe 壳（pc/mf801 主题）
 *
 * 与 pc/default 的实现逻辑一致，差异只在：
 *   - 挂载点：本主题根节点是 #app（而不是 .template）
 *   - 布局组件：common-layout（官方模板同款）
 */
(function (window, undefined) {
  var old_onload = window.onload;
  window.onload = function () {
    const template = document.getElementById("app");
    Vue.prototype.lang = Object.assign(window.lang, window.plugin_lang || {});

    new Vue({
      components: {
        commonLayout,
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
        /* 本主题没有 mainLoading 骨架，这里直接淡出页面自带的 loading 块 */
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
        /* iframe 加载完成 -> 关闭遮罩 */
        onFrameLoad() {
          this.loading = false;
        },
      },
    }).$mount(template);

    typeof old_onload == "function" && old_onload();
  };
})(window);
