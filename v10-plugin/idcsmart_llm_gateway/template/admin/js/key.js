/*
 * 后台 API Key 列表页
 *
 * Vue2 + TDesign。表格列在 data 里用 colKey 描述，单元格内容用 <template #colKey> 覆盖，
 * 与官方插件 template/admin/js/index.js 的写法保持一致。
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
          syncing: false,
          list: [],
          total: 0,
          params: {
            client_id: "",
            host_id: "",
            keyword: "",
            status: "",
            page: 1,
            limit: 20,
          },
          syncVisible: false,
          syncForm: { client_id: "" },
          columns: [
            { colKey: "key_id", title: lang.llm_gw_key_id, width: 110 },
            { colKey: "prefix", title: lang.llm_gw_key_prefix, width: 150 },
            { colKey: "name", title: lang.llm_gw_key_name, width: 180, ellipsis: true },
            { colKey: "client_id", title: lang.llm_gw_client_id, width: 100 },
            { colKey: "host_id", title: lang.llm_gw_host_id, width: 100 },
            { colKey: "status", title: lang.llm_gw_status, width: 110 },
            { colKey: "models", title: lang.llm_gw_models, width: 220 },
            { colKey: "ip_whitelist", title: lang.llm_gw_ip_whitelist, width: 180 },
            { colKey: "expires_at", title: lang.llm_gw_expires_at, width: 170 },
            { colKey: "created_at", title: lang.llm_gw_created_at, width: 170 },
            { colKey: "op", title: lang.llm_gw_operation, width: 180, fixed: "right" },
          ],
        };
      },
      created() {
        this.getList();
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
        /* unix 秒 -> 本地时间字符串 */
        formatTime(ts) {
          const d = new Date(Number(ts) * 1000);
          const pad = (n) => (n < 10 ? "0" + n : "" + n);
          return (
            d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) +
            " " + pad(d.getHours()) + ":" + pad(d.getMinutes())
          );
        },
        statusText(status) {
          if (status === "active") return lang.llm_gw_status_active;
          if (status === "suspended") return lang.llm_gw_status_suspended;
          return lang.llm_gw_status_revoked;
        },
        getList() {
          this.loading = true;
          const query = {};
          Object.keys(this.params).forEach((k) => {
            if (this.params[k] !== "" && this.params[k] !== null) {
              query[k] = this.params[k];
            }
          });
          getLlmKeyList(query)
            .then((res) => {
              if (res.data.status === 200) {
                this.list = res.data.data.list || [];
                this.total = res.data.data.count || 0;
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
        search() {
          this.params.page = 1;
          this.getList();
        },
        reset() {
          this.params = {
            client_id: "",
            host_id: "",
            keyword: "",
            status: "",
            page: 1,
            limit: this.params.limit,
          };
          this.getList();
        },
        onPageChange(pageInfo) {
          this.params.page = pageInfo.current;
          this.params.limit = pageInfo.pageSize;
          this.getList();
        },
        onPageSizeChange(pageSize, pageInfo) {
          this.params.page = 1;
          this.params.limit = pageSize;
          this.getList();
        },
        /* 封禁 / 解封 */
        changeStatus(row, status) {
          const that = this;
          const header = status === "suspended" ? lang.llm_gw_key_ban_confirm : lang.llm_gw_key_unban_confirm;
          let mydialog = this.$dialog({
            theme: status === "suspended" ? "warning" : "info",
            header: header,
            confirmBtn: lang.llm_gw_confirm,
            cancelBtn: lang.llm_gw_cancel,
            onConfirm: () => {
              mydialog.hide();
              updateLlmKeyStatus(row.key_id, { status: status })
                .then((res) => {
                  if (res.data.status === 200) {
                    that.tip(res.data.msg || lang.llm_gw_request_success);
                    that.getList();
                  } else {
                    that.warn(res.data.msg);
                  }
                })
                .catch((err) => {
                  that.fail(err.data ? err.data.msg : lang.llm_gw_request_failed);
                });
            },
          });
        },
        /* 吊销 */
        revokeKey(row) {
          const that = this;
          let mydialog = this.$dialog({
            theme: "danger",
            header: lang.llm_gw_key_revoke_confirm,
            confirmBtn: lang.llm_gw_confirm,
            cancelBtn: lang.llm_gw_cancel,
            onConfirm: () => {
              mydialog.hide();
              deleteLlmKey(row.key_id)
                .then((res) => {
                  if (res.data.status === 200) {
                    that.tip(res.data.msg || lang.llm_gw_request_success);
                    that.getList();
                  } else {
                    that.warn(res.data.msg);
                  }
                })
                .catch((err) => {
                  that.fail(err.data ? err.data.msg : lang.llm_gw_request_failed);
                });
            },
          });
        },
        /* 从网关刷新 */
        showSyncDialog() {
          this.syncForm.client_id = this.params.client_id || "";
          this.syncVisible = true;
        },
        doSync() {
          if (!this.syncForm.client_id) {
            this.warn(lang.llm_gw_client_id + lang.llm_gw_is_required);
            return;
          }
          this.syncing = true;
          syncLlmKeys({ client_id: this.syncForm.client_id })
            .then((res) => {
              if (res.data.status === 200) {
                this.tip(res.data.msg || lang.llm_gw_request_success);
                this.syncVisible = false;
                this.getList();
              } else {
                this.warn(res.data.msg);
              }
            })
            .catch((err) => {
              this.fail(err.data ? err.data.msg : lang.llm_gw_request_failed);
            })
            .finally(() => {
              this.syncing = false;
            });
        },
      },
    }).$mount(template);

    typeof old_onload == "function" && old_onload();
  };
})(window);
