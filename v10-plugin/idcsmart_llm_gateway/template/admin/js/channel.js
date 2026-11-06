/*
 * 后台渠道状态 / 模型目录页
 *
 * 边界：渠道增删改在网关后台；这里只读（plan §8.1、附录 C）。
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
          keyword: "",
          status: {
            ok: 0,
            version: "",
            db: "",
            cache: "",
            queue: "",
            error: "",
            flags: {},
          },
          models: [],
          columns: [
            { colKey: "model_name", title: lang.llm_gw_model_name, width: 200 },
            { colKey: "display_name", title: lang.llm_gw_model_display, width: 180, ellipsis: true },
            { colKey: "group_name", title: lang.llm_gw_model_group, width: 120 },
            { colKey: "context_len", title: lang.llm_gw_context_len, width: 130 },
            { colKey: "capabilities", title: lang.llm_gw_capabilities, width: 180 },
            { colKey: "model_ratio", title: lang.llm_gw_model_ratio, width: 110 },
            { colKey: "completion_ratio", title: lang.llm_gw_completion_ratio, width: 110 },
            { colKey: "price_input", title: lang.llm_gw_price_input, width: 150 },
            { colKey: "price_output", title: lang.llm_gw_price_output, width: 150 },
            { colKey: "status", title: lang.llm_gw_status, width: 90 },
          ],
        };
      },
      computed: {
        /* health.flags 是 {gateway_enabled:true,...}，转成数组便于 v-for */
        flagList() {
          const flags = this.status.flags || {};
          return Object.keys(flags).map((name) => ({ name: name, value: !!flags[name] }));
        },
      },
      created() {
        this.getStatus();
        this.getModels();
      },
      methods: {
        tip(content) {
          this.$message.success({ content: content, placement: "top-right" });
        },
        warn(content) {
          this.$message.warning({ content: content, placement: "top-right" });
        },
        fail(content) {
          this.$message.error({ content: content, placement: "top-right" });
        },
        /* 微元/百万 token -> 元/百万 token */
        priceText(row, key) {
          const hint = row.price_hint || {};
          if (hint[key] === undefined || hint[key] === null) {
            return "--";
          }
          return (Number(hint[key]) / 1000000).toFixed(4);
        },
        getStatus() {
          getLlmChannelStatus()
            .then((res) => {
              if (res.data.status === 200) {
                this.status = Object.assign(this.status, res.data.data || {});
              }
            })
            .catch((err) => {
              this.status.error = err.data ? err.data.msg : lang.llm_gw_gateway_unreachable;
            });
        },
        getModels() {
          this.loading = true;
          const params = {};
          if (this.keyword) {
            params.keyword = this.keyword;
          }
          getLlmModels(params)
            .then((res) => {
              if (res.data.status === 200) {
                this.models = res.data.data.list || [];
              } else {
                this.models = [];
                this.warn(res.data.msg);
              }
            })
            .catch((err) => {
              this.models = [];
              this.fail(err.data ? err.data.msg : lang.llm_gw_gateway_unreachable);
            })
            .finally(() => {
              this.loading = false;
            });
        },
        syncModels() {
          syncLlmModels()
            .then((res) => {
              if (res.data.status === 200) {
                const d = res.data.data || {};
                this.tip((d.count || 0) + " (" + (d.enabled || 0) + ")");
                this.getModels();
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
