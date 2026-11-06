# CZAPI 运维手册

面向部署与值守人员。目标是回答三个问题：**怎么起、怎么判断是否健康、出问题怎么退回。**

架构与计费口径见 `plan.md`；V10 侧约定见 `docs/v10-notes.md`；
接口契约见 `docs/internal-api-v1.md`（服务端）与 `docs/console-api-v1.md`（浏览器）。

---

## 1. 零依赖启动（最快验证路径）

网关默认使用内存驱动，**不需要 MySQL、不需要 Redis**。

```bash
go build -o czapi-server ./cmd/server
./czapi-server
```

首次启动会自动创建：
- 管理员 **`admin` / `admin`**（管理员表为空时才创建）
- 一个 V10 站点（app_id 默认 `v10`，密钥随机生成并**只打印一次**）

**`admin/admin` 必须在对外暴露前改掉。** 引导是幂等的：重启不会重置密码、
不会轮换站点密钥、不会重复插入演示数据。

启动后立即可用：

```bash
curl -s localhost:9999/healthz
# {"status":"ok","components":{"storage":"ok","cache":"ok","queue":"ok"}}

curl -s localhost:9999/readyz
```

内存驱动的语义必须理解清楚：

| 项 | 内存驱动行为 | 后果 |
| --- | --- | --- |
| 账户/Key/套餐/流水 | 进程内保存 | **重启即全部丢失** |
| 用量计数与 spend 计数器 | 进程内保存 | 重启后当日限额归零 |
| 队列 | 进程内保存 | 重启即丢失未消费用量 |

因此内存驱动**只用于开发、联调、演示、自动化测试**。生产必须用 mysql + redis。

---

## 2. 生产部署

### 2.1 前置

**上线前第一件事：改掉默认管理员密码。**

首次启动会用 `bootstrap.admin_password`（默认 `admin`）创建管理员。改成强密码后，
把 `bootstrap.admin_password` 设为空字符串，避免以后误建。

同时确认 `bootstrap.demo_content: false`（默认值）：演示渠道带的是占位符 key。

- MySQL 8.0+（utf8mb4）、Redis 6+（可选但强烈建议）
- 一个反向代理（Nginx/Caddy）负责 TLS
- `key_hash_salt` 与 `admin_jwt_secret` 必须是长随机值，且**salt 一旦上线不可更改**：
  所有已存 Key 哈希都依赖它，改了等于让所有客户 Key 失效。

生成密钥：

```bash
openssl rand -hex 32
```

### 2.2 建表

```bash
./scripts/migrate.sh "czapi:pass@tcp(127.0.0.1:3306)/czapi?charset=utf8mb4"
```

脚本会要求你输入数据库名二次确认。**不做自动迁移**是刻意的：迁移在滚动发布
中执行会破坏资金表。

### 2.3 配置

```bash
cp deploy/config.example.yaml config.yaml
```

至少要改：

```yaml
storage: { driver: mysql, dsn: "..." }
cache:   { driver: redis, addr: "127.0.0.1:6379" }
auth:
  key_hash_salt: "<64 hex>"
  admin_jwt_secret: "<32+ 字节随机值>"
internal:
  sites:
    - app_id: "v10-prod"
      secret: "<64 hex，V10 后台与此处必须一致>"
      enabled: true
```

所有项都支持环境变量覆盖，格式 `CZ_<SECTION>_<KEY>`，例如
`CZ_STORAGE_DSN`、`CZ_AUTH_KEY_HASH_SALT`。**环境变量优先于文件**，容器部署时用这个。

### 2.4 发布前检查

```bash
./scripts/preflight.sh config.yaml
```

任何一项 `FAIL` 都不要发布。它会拦住：格式/编译/测试失败、
`internal_allow_no_app: true`（这会移除 `/internal/v1` 的唯一鉴权）、
空 salt、空 JWT secret。

### 2.5 构建

```bash
./scripts/build.sh v1.2.3
```

产出 `dist/czapi-server`，前端已内联。**二进制保留上一版**：

```bash
cp /opt/czapi/czapi-server /opt/czapi/czapi-server.prev
```

---

## 3. 健康判断

| 端点 | 含义 | 认证 |
| --- | --- | --- |
| `GET /healthz` | 存活 | 无 |
| `GET /readyz` | 就绪（storage 必须 ok） | 无 |
| `GET /metrics` | Prometheus 指标 | 无 |
| `GET /internal/v1/health` | V10 视角的连通性 | 无（签名组的例外） |

`/readyz` 的判定口径：**storage 必须 ok**；cache 与 queue 故障只降级不摘流量。
这是刻意的——缓存挂了不该让整个网关下线。

### 3.1 必须配置的三条告警

| 告警 | 条件 | 含义 |
| --- | --- | --- |
| 队列积压 | `czapi_queue_depth > 1000` 持续 5 分钟 | 用量落库跟不上，账目会延迟 |
| 计费延迟 | `czapi_billing_lag_seconds > 60` | 结算变慢，可能积压 |
| 渠道熔断 | `czapi_channel_open == 1` 持续 2 分钟 | 上游不可用，用户会收到 5xx |

### 3.2 其余值得盯的指标

```
czapi_requests_total{status,model}       # 流量与错误率
czapi_quota_rejected_total{reason}       # 被限额拒绝的量（reason=estimate_overdraw 代表预扣爆掉）
czapi_channel_errors_total{channel}      # 单渠道错误
czapi_reconcile_diff_micros              # 对账差异，非 0 要查
czapi_credit_total{result}               # 入账结果（applied / duplicate / rejected）
```

---

## 4. 资金安全机制（理解这些再改配置）

网关**从不经手支付**。V10 负责下单、支付、退款、卡密；网关只做**幂等入账**。

四条不变量：

1. **入账幂等**：账本唯一键 `(ref_type, ref_id)`。V10 重推同一笔订单不会重复加钱。
2. **用量幂等**：用量表唯一键 `request_id`。重复消费不会重复记账。
3. **订阅幂等**：订阅唯一键 `v10_host_id`。重复开通不会开出两份。
4. **预扣—结算**：先按估算预扣，再按实际用量结算，多扣的退回。

所以**修复账目问题的正确方式是重放请求，不是写 SQL**。重放安全，手改危险。

### 4.1 三个刹车

| 刹车 | 位置 | 作用 |
| --- | --- | --- |
| `billing_enforced=false` | 运行时 flag | 全站停止扣费，仍计量。**首选应急手段** |
| `key_daily_micros` | 配置 | 单 Key 日消费上限，默认 50 元。Key 泄漏的爆炸半径 |
| `account_monthly_micros` | 配置 | 单账户月消费上限，默认 500 元 |

`billing_enforced` 是**热加载**的（`FlagStore`），改完下一个请求即生效，不需要重启。
这是应急时最重要的一个开关。

---

## 5. 应急：出问题怎么办

**先止血，再定位。顺序不能反。**

### 5.1 疑似计费错误

```bash
# 1. 立刻停止扣费（不停止服务）
curl -X POST localhost:9999/admin/v1/flags/billing_enforced \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"value": false}'

# 2. 确认已生效
curl -s localhost:9999/admin/v1/flags -H "Authorization: Bearer $TOKEN"
```

此时所有请求仍被计量、仍写用量行，但结算为 `PaidByFree`，**不动任何客户余额**。
服务不中断。

### 5.2 回滚二进制

```bash
./scripts/rollback.sh /opt/czapi czapi
```

脚本会按顺序引导：先关计费 → 换回上一版 → 验证 → 再决定是否开计费。
中途每一步都要人工确认，因为**在计费错误未查清前重新开计费是危险的**。

### 5.3 对账

```bash
curl -s "localhost:9999/admin/v1/reconcile?day=2026-11-05" \
  -H "Authorization: Bearer $TOKEN"
```

返回三条链路的总量：原始用量、日汇总、账本，以及按账户的差异。
**只有账本总量影响钱**。若差异非零，先查清是哪条链路漏写，再重放对应请求。

### 5.4 渠道故障

```bash
# 单渠道连通性测试
curl -X POST localhost:9999/admin/v1/channels/1/test -H "Authorization: Bearer $TOKEN"

# 熔断器重置（确认上游已恢复后）
curl -X POST localhost:9999/admin/v1/channels/1/reset-breaker -H "Authorization: Bearer $TOKEN"
```

熔断是自动的：错误率超阈值会短时间内不再打该渠道，并切到其他渠道。
**不要因为熔断就重启网关**，它自己会恢复。

### 5.5 关闭整个网关

```bash
curl -X POST localhost:9999/admin/v1/flags/gateway_enabled \
  -H "Authorization: Bearer $TOKEN" -d '{"value": false}'
```

返回 503 `feature disabled`。比停进程好：健康检查仍通过，V10 侧能看到明确原因。

---

## 6. 前端控制台

- 构建：`cd web && pnpm install && pnpm build`，产物落到 `internal/api/console/dist`
- 访问：`/console/`，需要 V10 下发的一次性票据（`/console/v10-sso?ticket=...`）
- **未构建不是故障**：`/console` 会返回 JSON 提示而非 500，网关其余功能不受影响

票据机制：V10 后端调 `/internal/v1/sso/ticket` 拿到 60 秒一次性票据，
浏览器带票据进 `/console/v10-sso` 换会话 Cookie。票据绑定 client_id，用后作废。

---

## 7. 常见问题

**Q: `insufficient_quota` 但客户说已付款。**
查账本：`GET /admin/v1/ledger?account_id=N`。若没有对应入账，
说明 V10 的入账回调没打过来——去 V10 后台看推送日志并重推。重推是安全的。

**Q: 客户用 `PaidByFree` 结算，是不是漏收了？**
先查 `billing_enforced` 是否被关了。这是全站开关，可能有人应急后忘了恢复。

**Q: 重启后用量统计变小了。**
用的内存驱动。生产必须配 mysql。

**Q: 403 `model_not_allowed`。**
模型不存在或未启用，或 Key 的模型白名单不包含它。网关**不会**盲目转发未知模型。

**Q: `/console` 返回 JSON 提示。**
前端未构建。按 §6 构建即可，不影响 API。

**Q: Redis 挂了会怎样？**
限流与 Key 缓存失效**放行**（fail-open），请求继续服务；用量写入退回同步。
可用性优先是刻意的设计选择，代价是故障期间限流不严格。

---

## 8. 已知未验证项

诚实记录，上线前需逐条确认：

- **MySQL / Redis 驱动未做集成测试**：编译通过、SQL 构造有单元测试，
  但**没有在真实实例上跑过**。生产前必须用真实库跑一轮完整读写。
- **`go test -race` 无法运行**：本机无 cgo 工具链。并发正确性靠代码审查与
  内存驱动的锁保护，未做竞态检测。
- **V10 插件 22 项待实例验证**：见 `v10/README.md` §11。最关键的是
  `order_paid` 事件名未经实例确认，它是充值入账的唯一触发点。
- **易支付通道**：签名与回调流程按文档实现，未对接真实商户号验证。
