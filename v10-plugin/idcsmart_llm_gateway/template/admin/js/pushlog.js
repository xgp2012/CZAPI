/*
 * 后台推送队列页
 *
 * 对应 plan §10.3 的可靠投递：V10 入账 -> 先写 _pushlog -> 调网关 /internal/v10/credit
 * -> 失败指数退避重试。本页是运维的"自助修复入口"：
 * 客户投诉"充了没到账"时，按业务单号搜到记录点"重投"，
 * 幂等由网关 (ref_type, ref_id) 保证，重投不会重复加额。
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
          retrying: false,
          list: [],
          total: 0,
          params: {
            status: "",
            ref_id: "",
            page: 1,
            limit: 20,
          },
          columns: [
            { colKey: "id", title: "ID", width: 80 },
            { colKey: "endpoint", title: lang.llm_gw_pushlog_endpoint, width: 200, ellipsis: true },
            { colKey: "ref_type", title: "ref_type", width: 120 },
            { colKey: "ref_id", title: lang.llm_gw_pushlog_ref, width: 160, ellipsis: true },
            { colKey: "attempts", title: lang.llm_gw_pushlog_attempts, width: 100 },
            { colKey: "status", title: lang.llm_gw_pushlog_status, width: 110 },
            { colKey: "next_retry_at", title: lang.llm_gw_pushlog_next, width: 170 },
            { colKey: "last_error", title: lang.llm_gw_pushlog_error, width: 300 },
            { colKey: "create_time", title: lang.llm_gw_created_at, width: 170 },
            { colKey: "op", title: lang.llm_gw_operation, width: 100, fixed: "right" },
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
        formatTime(ts) {
          const n = Number(ts);
          if (!n) {
            return "--";
          }
          const d = new Date(n * 1000);
          const pad = (x) => (x < 10 ? "0" + x : "" + x);
          return (
            d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) +
            " " + pad(d.getHours()) + ":" + pad(d.getMinutes())
          );
        },
        statusText(status) {
          if (status === "done") return lang.llm_gw_pushlog_done;
          if (status === "pending") return lang.llm_gw_pushlog_pending;
          return lang.llm_gw_pushlog_failed;
        },
        getList() {
          this.loading = true;
          const query = { page: this.params.page, limit: this.params.limit };
          if (this.params.status) {
            query.status = this.params.status;
          }
          if (this.params.ref_id) {
            query.ref_id = this.params.ref_id;
          }
          getLlmPushLog(query)
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
          this.params = { status: "", ref_id: "", page: 1, limit: this.params.limit };
          this.getList();
        },
        onPageChange(pageInfo) {
          this.params.page = pageInfo.current;
          this.params.limit = pageInfo.pageSize;
          this.getList();
        },
        retryOne(row) {
          const that = this;
          let mydialog = this.$dialog({
            theme: "warning",
            header: lang.llm_gw_pushlog_retry + " #" + row.id,
            body: row.last_error || "",
            confirmBtn: lang.llm_gw_confirm,
            cancelBtn: lang.llm_gw_cancel,
            onConfirm: () => {
              mydialog.hide();
              retryLlmPushLog(row.id)
                .then((res) => {
                  if (res.data.status === 200) {
                    that.tip(res.data.msg || lang.llm_gw_request_success);
                  } else {
                    that.warn(res.data.msg);
                  }
                  that.getList();
                })
                .catch((err) => {
                  that.fail(err.data ? err.data.msg : lang.llm_gw_request_failed);
                  that.getList();
                });
            },
          });
        },
        retryAll() {
          this.retrying = true;
          retryAllLlmPushLog({ limit: 50 })
            .then((res) => {
              if (res.data.status === 200) {
                const d = res.data.data || {};
                this.tip(
                  (d.scanned || 0) + " / " + (d.done || 0) + " / " + (d.failed || 0)
                );
                this.getList();
              } else {
                this.warn(res.data.msg);
              }
            })
            .catch((err) => {
              this.fail(err.data ? err.data.msg : lang.llm_gw_request_failed);
            })
            .finally(() => {
              this.retrying = false;
            });
        },
      },
    }).$mount(template);

    typeof old_onload == "function" && old_onload();
  };
})(window);
