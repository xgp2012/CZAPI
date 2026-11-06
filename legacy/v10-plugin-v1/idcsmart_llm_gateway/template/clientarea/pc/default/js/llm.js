/*
 * LLM 网关 —— iframe 壳（pc/default 主题）
 *
 * 为什么是 iframe（plan §8.5）：
 *   V10 前台是 Vue2 + Element UI，而我们的网关控制台是 Vue3，
 *   两套 VDOM 无法挂在同一个 root 上。最稳的做法是在 V10 页面里放一个 iframe，
 *   用一次性 SSO 票据把用户带进 Vue3 控制台，互不干扰、也不会随主题升级而失效。
 *
 * 本文件做三件事：
 *   1. GET console/v1/llm/sso-ticket（路由组挂 CheckHome，需登录态）
 *   2. 拿到 data.sso_url 赋给 iframe.src（票据一次性、60 秒过期、绑定 client_id）
 *   3. iframe onload 之后隐藏 loading 遮罩
 *
 * 注意：票据只在服务端换取，前端拿不到 app_secret，也不直接接触网关的签名接口。
 */
(function (window, undefined) {
  var old_onload = window.onload;
  window.onload = function () {
    const template = document.getElementsByClassName("template")[0];
    Vue.prototype.lang = Object.assign(window.lang, window.plugin_lang || {});

    new Vue({
      components: {
        asideMenu,
        topMenu,
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
      updated() {
        /* 与官方主题一致：Vue 首次渲染后关掉骨架 loading */
        const loadingEl = document.getElementById("mainLoading");
        if (loadingEl) {
          loadingEl.style.display = "none";
        }
        if (template) {
          template.style.display = "block";
        }
      },
      methods: {
        /* 申请一次性票据 */
        loadTicket() {
          this.loading = true;
          this.errorMsg = "";

          /* Axios 是 V10 前台的全局实例，baseURL 已指向 /console/v1，并自带 JWT 头 */
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
          if (!this.loading) {
            return;
          }
          this.loading = false;
          const loadingEl = document.getElementById("mainLoading");
          if (loadingEl) {
            loadingEl.style.display = "none";
          }
        },
      },
    }).$mount(template);

    typeof old_onload == "function" && old_onload();
  };
})(window);
