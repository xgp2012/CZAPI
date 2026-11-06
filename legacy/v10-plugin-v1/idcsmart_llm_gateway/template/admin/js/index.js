/*
 * 后台仪表盘页
 *
 * 页面机制（源码实证，docs/v10-notes.md §5）：
 *   HTML 片段 + 独立 JS 创建 Vue 实例；后台全局是 Vue2 + TDesign（t-*），
 *   Vue.prototype.lang = Object.assign(window.lang, window.plugin_lang)。
 *
 * 提示与弹窗沿用官方插件的写法：
 *   this.$message.success({ content, placement: "top-right" })
 *   this.$dialog({ theme, header, confirmBtn, cancelBtn, onConfirm })  ← TDesign DialogPlugin
 */
(function (window, undefined) {
  var old_onload = window.onload;
  window.onload = function () {
    const template = document.getElementsByClassName("template")[0];
    Vue.prototype.lang = Object.assign(window.lang, window.plugin_lang);

    new Vue({
      data() {
        return {
          loading: false,
          dashboard: {
            health_ok: false,
            health: {},
            key_total: 0,
            key_active: 0,
            key_suspended: 0,
            key_revoked: 0,
            push_failed: 0,
            push_pending: 0,
            redeem_total: 0,
          },
          config: {
            gateway_base_url: "",
            app_id: "",
            app_secret_tail: "",
            keep_data_on_uninstall: "1",
          },
        };
      },
      created() {
        this.loadDashboard();
        this.loadConfig();
      },
      methods: {
        /* 统一提示封装，避免每处都写 placement */
        tip(content) {
          this.$message.success({ content: content, placement: "top-right" });
        },
        warn(content) {
          this.$message.warning({ content: content, placement: "top-right" });
        },
        fail(content) {
          this.$message.error({ content: content, placement: "top-right" });
        },
        /* 仪表盘数据 */
        loadDashboard() {
          this.loading = true;
          getLlmDashboard()
            .then((res) => {
              if (res.data.status === 200) {
                const data = res.data.data || {};
                data.health = data.health || {};
                this.dashboard = Object.assign(this.dashboard, data);
              } else {
                this.warn(res.data.msg);
              }
            })
            .catch((err) => {
              this.fail(err.data ? err.data.msg : lang.llm_gw_request_failed);
            })
            .finally(() => {
              this.loading = false;
            });
        },
        /* 配置（脱敏只读） */
        loadConfig() {
          getLlmConfig()
            .then((res) => {
              if (res.data.status === 200) {
                this.config = Object.assign(this.config, res.data.data || {});
              }
            })
            .catch(() => {
              /* 配置读取失败不影响仪表盘可用性 */
            });
        },
        /* 卸载策略：切到"彻底卸载"必须二次确认 */
        changeUninstallPolicy(value) {
          const next = String(value);
          if (next === "0") {
            const that = this;
            let mydialog = this.$dialog({
              theme: "warning",
              header: lang.llm_gw_uninstall_policy,
              body: lang.llm_gw_uninstall_drop + " — " + lang.llm_gw_uninstall_tip,
              confirmBtn: lang.llm_gw_confirm,
              cancelBtn: lang.llm_gw_cancel,
              onConfirm: () => {
                mydialog.hide();
                that.saveUninstallPolicy("0");
              },
              onCancel: () => {
                mydialog.hide();
                that.config.keep_data_on_uninstall = "1";
              },
            });
            return;
          }
          this.saveUninstallPolicy("1");
        },
        saveUninstallPolicy(value) {
          const rollback = value === "0" ? "1" : "0";
          setLlmConfig({ keep_data_on_uninstall: value })
            .then((res) => {
              if (res.data.status === 200) {
                this.config.keep_data_on_uninstall = value;
                this.tip(res.data.msg || lang.llm_gw_request_success);
              } else {
                this.config.keep_data_on_uninstall = rollback;
                this.warn(res.data.msg);
              }
            })
            .catch((err) => {
              this.config.keep_data_on_uninstall = rollback;
              this.fail(err.data ? err.data.msg : lang.llm_gw_request_failed);
            });
        },
        /* 跳转 V10 自带的插件配置页（表单由 config.php 自动渲染，插件不写配置页） */
        goConfigPage() {
          /*
           * ⚠️ 待实例验证: 官方 idcsmart_ticket 4.2.2 里没有"跳转到插件配置页"的代码，
           *   插件配置由 V10 后台的插件列表进入。这里用相对路径回到插件列表页作为最保守做法，
           *   避免猜一个不存在的 URL 造成 404；若实例确认了配置页路径，再替换成直达链接。
           */
          window.location.href = "../plugin/index.html";
        },
        /* 批量重投推送队列 */
        retryAll() {
          retryAllLlmPushLog({ limit: 50 })
            .then((res) => {
              if (res.data.status === 200) {
                const d = res.data.data || {};
                this.tip(
                  lang.llm_gw_pushlog_retry_all +
                    ": scanned " + (d.scanned || 0) +
                    ", done " + (d.done || 0) +
                    ", failed " + (d.failed || 0)
                );
                this.loadDashboard();
              } else {
                this.warn(res.data.msg);
              }
            })
            .catch((err) => {
              this.fail(err.data ? err.data.msg : lang.llm_gw_request_failed);
            });
        },
      },
    }).$mount(template);

    typeof old_onload == "function" && old_onload();
  };
})(window);
