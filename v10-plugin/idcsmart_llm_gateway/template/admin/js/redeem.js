/*
 * 后台卡密批次页
 *
 * 关键点：明文卡密只在"生成批次"这一次由后端返回，之后库里只有 sha256 与前缀
 * （plan §10.2）。因此这条路径必须提供"复制 / 导出 CSV"，让运营当场保存。
 */
(function (window, undefined) {
  var old_onload = window.onload;
  window.onload = function () {
    const template = document.getElementsByClassName("template")[0];
    Vue.prototype.lang = Object.assign(window.lang, window.plugin_lang);

    new Vue({
      data() {
        return {
          /* 生成表单 */
          creating: false,
          form: {
            count: "",
            amount_yuan: "",
            batch_no: "",
            expires_at: "",
          },
          /* 批次列表 */
          loading: false,
          batches: [],
          total: 0,
          params: { batch_no: "", page: 1, limit: 20 },
          batchColumns: [
            { colKey: "batch_no", title: lang.llm_gw_batch_no, width: 220, ellipsis: true },
            { colKey: "total", title: lang.llm_gw_batch_total, width: 100 },
            { colKey: "unused", title: lang.llm_gw_batch_unused, width: 100 },
            { colKey: "value_micros", title: lang.llm_gw_batch_value, width: 160 },
            { colKey: "create_time", title: lang.llm_gw_created_at, width: 170 },
            { colKey: "op", title: lang.llm_gw_operation, width: 320, fixed: "right" },
          ],
          /* 明文弹窗 */
          codeVisible: false,
          plainCodes: [],
          plainBatchNo: "",
          /* 明细弹窗 */
          codesVisible: false,
          codesLoading: false,
          codes: [],
          codesTotal: 0,
          codesParams: { batch_no: "", page: 1, limit: 20 },
          codeColumns: [
            { colKey: "id", title: "ID", width: 80 },
            { colKey: "code_prefix", title: lang.llm_gw_code_prefix, width: 120 },
            { colKey: "value_micros", title: lang.llm_gw_code_value, width: 120 },
            { colKey: "redeemed_by", title: lang.llm_gw_code_redeemed_by, width: 120 },
            { colKey: "redeemed_at", title: lang.llm_gw_code_redeemed_at, width: 170 },
            { colKey: "status", title: lang.llm_gw_status, width: 90 },
          ],
          /* 流水弹窗 */
          txnVisible: false,
          txnLoading: false,
          txns: [],
          txnTotal: 0,
          txnParams: { page: 1, limit: 20 },
          txnColumns: [
            { colKey: "id", title: "ID", width: 80 },
            { colKey: "client_id", title: lang.llm_gw_client_id, width: 100 },
            { colKey: "amount_micros", title: lang.llm_gw_txn_amount, width: 130 },
            { colKey: "ref_no", title: lang.llm_gw_txn_ref, width: 120 },
            { colKey: "status", title: lang.llm_gw_txn_status, width: 110 },
            { colKey: "create_time", title: lang.llm_gw_created_at, width: 170 },
          ],
        };
      },
      computed: {
        plainCodesText() {
          return this.plainCodes.join("\n");
        },
      },
      created() {
        this.getBatches();
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
        /* 微元 -> 元（保留 2 位展示） */
        micros2yuan(micros) {
          return (Number(micros || 0) / 1000000).toFixed(2);
        },
        /* ===== 生成 ===== */
        createBatch() {
          const count = parseInt(this.form.count, 10);
          const amount = parseFloat(this.form.amount_yuan);

          if (!count || count < 1) {
            this.warn(lang.llm_gw_batch_count + lang.llm_gw_is_required);
            return;
          }
          if (!amount || amount <= 0) {
            this.warn(lang.llm_gw_batch_amount + lang.llm_gw_is_required);
            return;
          }

          const payload = { count: count, amount_yuan: amount };
          if (this.form.batch_no) {
            payload.batch_no = this.form.batch_no;
          }
          const expires = parseInt(this.form.expires_at, 10);
          if (expires > 0) {
            payload.expires_at = expires;
          }

          this.creating = true;
          createLlmRedeemBatch(payload)
            .then((res) => {
              if (res.data.status === 200) {
                const d = res.data.data || {};
                this.plainCodes = d.codes || [];
                this.plainBatchNo = d.batch_no || "";
                this.codeVisible = true;
                this.form.count = "";
                this.form.amount_yuan = "";
                this.form.batch_no = "";
                this.form.expires_at = "";
                this.getBatches();
              } else {
                this.warn(res.data.msg);
              }
            })
            .catch((err) => {
              this.fail(err.data ? err.data.msg : lang.llm_gw_request_failed);
            })
            .finally(() => {
              this.creating = false;
            });
        },
        /* 复制明文（优先 Clipboard API，失败退回复选 textarea） */
        copyCodes() {
          const text = this.plainCodesText;
          if (!text) {
            return;
          }
          const that = this;
          if (navigator.clipboard && navigator.clipboard.writeText) {
            navigator.clipboard.writeText(text).then(
              () => that.tip(lang.llm_gw_copied),
              () => that.fallbackCopy(text)
            );
          } else {
            this.fallbackCopy(text);
          }
        },
        fallbackCopy(text) {
          const ta = document.createElement("textarea");
          ta.value = text;
          ta.style.position = "fixed";
          ta.style.opacity = "0";
          document.body.appendChild(ta);
          ta.select();
          try {
            document.execCommand("copy");
            this.tip(lang.llm_gw_copied);
          } catch (e) {
            this.warn(lang.llm_gw_request_failed);
          }
          document.body.removeChild(ta);
        },
        /* 导出 CSV（带 BOM，Excel 打开不乱码） */
        exportCsv() {
          if (!this.plainCodes.length) {
            return;
          }
          const rows = ["batch_no,code"];
          this.plainCodes.forEach((code) => {
            rows.push(this.plainBatchNo + "," + code);
          });
          const blob = new Blob(["\ufeff" + rows.join("\r\n")], {
            type: "text/csv;charset=utf-8;",
          });
          const url = window.URL.createObjectURL(blob);
          const a = document.createElement("a");
          a.href = url;
          a.download = "llm_redeem_" + (this.plainBatchNo || "batch") + ".csv";
          document.body.appendChild(a);
          a.click();
          document.body.removeChild(a);
          window.URL.revokeObjectURL(url);
        },
        /* ===== 列表 ===== */
        getBatches() {
          this.loading = true;
          const query = { page: this.params.page, limit: this.params.limit };
          if (this.params.batch_no) {
            query.batch_no = this.params.batch_no;
          }
          getLlmRedeemBatchList(query)
            .then((res) => {
              if (res.data.status === 200) {
                this.batches = res.data.data.list || [];
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
          this.getBatches();
        },
        reset() {
          this.params = { batch_no: "", page: 1, limit: this.params.limit };
          this.getBatches();
        },
        onPageChange(pageInfo) {
          this.params.page = pageInfo.current;
          this.params.limit = pageInfo.pageSize;
          this.getBatches();
        },
        /* 查看某批次的卡密明细 */
        viewCodes(row) {
          this.codesParams.batch_no = row.batch_no;
          this.codesParams.page = 1;
          this.codesVisible = true;
          this.getCodes();
        },
        getCodes() {
          this.codesLoading = true;
          getLlmRedeemCodeList({
            batch_no: this.codesParams.batch_no,
            page: this.codesParams.page,
            limit: this.codesParams.limit,
          })
            .then((res) => {
              if (res.data.status === 200) {
                this.codes = res.data.data.list || [];
                this.codesTotal = res.data.data.count || 0;
              } else {
                this.warn(res.data.msg);
              }
            })
            .catch((err) => {
              this.fail(err.data ? err.data.msg : lang.llm_gw_request_failed);
            })
            .finally(() => {
              this.codesLoading = false;
            });
        },
        onCodesPageChange(pageInfo) {
          this.codesParams.page = pageInfo.current;
          this.codesParams.limit = pageInfo.pageSize;
          this.getCodes();
        },
        /* 批次启用 / 禁用 */
        disableBatch(row) {
          this.setBatchStatus(row.batch_no, 0);
        },
        enableBatch(row) {
          this.setBatchStatus(row.batch_no, 1);
        },
        setBatchStatus(batchNo, status) {
          const that = this;
          let mydialog = this.$dialog({
            theme: status === 0 ? "warning" : "info",
            header: status === 0 ? lang.llm_gw_batch_disable : lang.llm_gw_batch_enable,
            confirmBtn: lang.llm_gw_confirm,
            cancelBtn: lang.llm_gw_cancel,
            onConfirm: () => {
              mydialog.hide();
              setLlmRedeemBatchStatus({ batch_no: batchNo, status: status })
                .then((res) => {
                  if (res.data.status === 200) {
                    that.tip(res.data.msg || lang.llm_gw_request_success);
                    that.getBatches();
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
        /* ===== 兑换流水 ===== */
        showTxn() {
          this.txnVisible = true;
          this.txnParams.page = 1;
          this.getTxns();
        },
        getTxns() {
          this.txnLoading = true;
          getLlmRedeemTxn({ page: this.txnParams.page, limit: this.txnParams.limit })
            .then((res) => {
              if (res.data.status === 200) {
                this.txns = res.data.data.list || [];
                this.txnTotal = res.data.data.count || 0;
              } else {
                this.warn(res.data.msg);
              }
            })
            .catch((err) => {
              this.fail(err.data ? err.data.msg : lang.llm_gw_request_failed);
            })
            .finally(() => {
              this.txnLoading = false;
            });
        },
        onTxnPageChange(pageInfo) {
          this.txnParams.page = pageInfo.current;
          this.txnParams.limit = pageInfo.pageSize;
          this.getTxns();
        },
      },
    }).$mount(template);

    typeof old_onload == "function" && old_onload();
  };
})(window);
