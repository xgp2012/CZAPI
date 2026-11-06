# CZAPI 并行开发分工与工程约定

> 本文件是多个子代理并行开发时的**文件所有权地图**。任何代理**只能修改自己名下的路径**，
> 需要改公共契约时必须停下来报告，不得擅自修改。

---

## 0. 环境事实（实测，勿再验证）

| 项 | 值 |
| --- | --- |
| Go | 1.27.1 windows/amd64，`GOFLAGS=-mod=mod`，模块名 `czapi` |
| 可用代理 | `GOPROXY=https://goproxy.cn,direct`（已验证可下载） |
| Node / pnpm | Node 24.22，`pnpm` 在 PATH |
| PHP | 8.3.33 CLI（无 MySQL 扩展、无 Web 服务） |
| Docker / MySQL / Redis / make / gcc | **均不可用** |

**由此产生的硬约束（保证可用性的核心策略）**：

1. 网关必须能在 **纯内存驱动**（`storage.driver=memory` + `cache.driver=memory`）下完整启动并通过集成测试。
   MySQL / Redis 是生产目标，不是本地开发的必要条件。
2. 不允许引入需要 cgo 的依赖（无 gcc）。
3. 不允许引入需要外部服务的测试依赖（单测必须 `go test ./...` 直接过）。
4. 所有依赖必须能通过 `goproxy.cn` 拉到；新增第三方库前先 `go get` 试一次。

---

## 1. 冻结文件（**只读**，禁止修改）

| 路径 | 内容 |
| --- | --- |
| `plan.md` | 需求与设计 |
| `docs/v10-notes.md` | V10 插件权威笔记 |
| `docs/internal-api-v1.md` | `/internal/v1` 接口契约 v1 |
| `internal/config/config.go` | 配置结构与默认值 |
| `internal/domain/types.go` | 领域模型 |
| `internal/store/store.go` | 持久化接口 |
| `internal/cache/cache.go` | 低延迟状态接口 |
| `internal/queue/queue.go` | 异步队列接口 |
| `internal/billing/billing.go` | 计费接口与 DTO |
| `internal/relay/relay.go` | 转发接口与 DTO |
| `internal/pkg/{log,errs,idgen,clock,sign,crypto}/*` | 公共工具 |
| `migrations/0001_init.sql` | 表结构 |

若发现冻结文件有缺陷，**在报告中说明**，不要直接改。

---

## 2. 目录所有权

```
cmd/server, cmd/worker            → WS-WIRE（收尾，由主控执行）
internal/platform/                → WS-WIRE
internal/api/                     → WS-API
internal/auth/                    → WS-AUTH
internal/quota/, internal/worker/ → WS-SETTLE
internal/billing/engine*.go       → WS-ENGINE
internal/relay/, internal/provider/ → WS-RELAY
internal/store/memstore/, cache/memcache/, queue/memqueue/ → WS-MEM
internal/store/mysqlstore/, cache/rediscache/, queue/redisqueue/, internal/migrate/ → WS-SQL
internal/metrics/                 → WS-WIRE
web/                              → WS-WEB
v10/                              → WS-PHP
deploy/, scripts/, docs/runbook.md, docs/v10-*.md 其余 → WS-WIRE
```

一个代理**只写自己那一行**的路径。跨包引用一律通过冻结接口（`store.Store`、`cache.Cache`、`queue.Queue`、`billing.Engine`、`billing.Ledger`、`relay.Forwarder`）。

---

## 3. 代码风格（强制）

- 注释用**英文**，`godoc` 风格，包级包注释说明用途；**导出的每个标识符都要有注释**。
- 错误一律用 `internal/pkg/errs`：`errs.NotFound("...")` 等，不要裸 `errors.New` 穿透到 API 层。
- 时间统一 `internal/pkg/clock.Clock`（可注入），不要直接 `time.Now()`（`cmd/` 与测试除外）。
- ID 统一 `internal/pkg/idgen.ULID()`。
- 并发安全：实现必须显式说明是否并发安全（注释里写 `Safe for concurrent use.`）。
- 不要 `panic`（初始化路径除外），不要 `log.Fatal` 在库代码里。
- 每个实现包**必须带 `_test.go`**，且测试能在无外部服务下跑通。
- 金额单位：**微元整数**（1 元 = 1_000_000）。额度单位：**token 整数**。
- `gofmt -w` + `go vet ./...` 必须干净。

---

## 4. 质量门槛（每个工作流交付前自检）

1. `go build ./...` 通过。
2. `go vet ./...` 无输出。
3. `go test ./<自己的包>/...` 全绿，且覆盖关键分支。
4. 没有 `TODO`/`FIXME` 留在主路径（可选增强才允许 `// TODO(enhancement): ...`）。
5. 报告里写清：改了哪些文件、对外暴露的构造函数签名、已知限制。

---

## 5. 关键语义（各代理都要遵守，避免实现漂移）

### 5.1 幂等
- `LedgerRepo.Credit` 对 `(ref_type, ref_id)` 唯一：重复调用返回 `applied=false`，余额不变。**并发安全**（同一凭证并发 10 次只加一次）。
- `UsageRepo.Insert` 对 `request_id` 唯一。
- `SubscriptionRepo.Create` 对 `v10_host_id` 唯一，返回 `created=false`。

### 5.2 计费
```
cost_tokens = (prompt + completion*completion_ratio − cached*(1−cached_ratio)) * model_ratio * group_ratio
cost_micros = ceil(cost_tokens * token_to_micros)      // int64，上限保护
```
- `cost_micros` 下界 0；`completion` 为 0 时不得为负。
- 若 `input_micros`/`output_micros` 非 0，则改用**绝对价**：
  `cost_micros = ceil(prompt*input_micros/1e6 + completion*output_micros/1e6 − cached*input_micros*(1−cached_ratio)/1e6)`。
- 溢出保护：任何中间结果超过 `9e18` 视为异常，钳制到 `math.MaxInt64/2`。

### 5.3 预扣/结算
- 预扣额 = `max(estimate_micros, 一个最小值)`，不得小于实际可能消费。
- 结算：`refund = held − actual`（≥0 才退；实际超出预扣时补扣，允许余额转负）。
- 流式中断（`Partial=true`）：按已观测到的 usage 结算；无 usage 则按预扣全额结算。

### 5.4 功能开关
`Flags.GatewayEnabled=false` → `/v1/*` 全部返回 `503 feature_disabled`（健康检查与 `/internal/v1/health` 不受影响）。
`Billing.EnforceBilling=false` → 照常计量与落库，但**不扣任何额度**（记账 `billing_mode=PaidByFree`）。

---

## 6. 各工作流交付清单

### WS-MEM：内存驱动（`internal/store/memstore`、`internal/cache/memcache`、`internal/queue/memqueue`）
- 完整实现 `store.Store` / `cache.Cache` / `queue.Queue`，语义与注释一致。
- 构造函数：`memstore.New() *memstore.Store`、`memcache.New() *memcache.Cache`、`memqueue.New() *memqueue.Queue`。
- 必须实现 5.1 的幂等与并发安全（用 `sync.RWMutex`）。
- `memqueue` 用带缓冲 channel + 重试计数；`Consume` 支持 `batch` 与 `wait`。
- 测试：幂等（同凭证 10 并发只加一次）、额度汇总、过滤器、并发限流、nonce 重放。

### WS-ENGINE：计费引擎（`internal/billing/engine.go`、`estimate.go`、`engine_test.go`）
- `func NewEngine(cfg EngineConfig) Engine`，纯函数，无 I/O。
- `EstimateTokens`：不引入 tokenizer 依赖，用启发式（ASCII≈4 字符/token，CJK≈1.5 字符/token，混合按字符类加权），永不 panic。
- 测试 ≥ 30 条用例，覆盖：0 token、仅 prompt、仅 completion、cached 折扣、绝对价、倍率组合、极大值溢出、边界。

### WS-RELAY：转发与路由（`internal/relay/*_impl.go`、`selector.go`、`breaker.go`、`internal/provider/openai`）
- 实现 `relay.Forwarder` 与 `relay.Selector`。
- 渠道选择：`priority` 分层 → 层内加权随机 → 过滤熔断/并发满。
- 重试：连接错误/超时/5xx 换渠道 ≤`MaxRetries`；4xx（429 除外）不重试；429 可换一次；**已向客户端写出首字节后不重试**。
- SSE：`text/event-stream` 逐块读 + `Flush()`，末尾透传 `[DONE]`，解析 `usage`（含 `stream_options.include_usage`）。
- 熔断器：滑动窗口，参数来自 `config.Relay`。
- 测试：`httptest` 假上游，覆盖成功/5xx 重试/429/超时/流式中断/usage 解析。

### WS-AUTH：鉴权（`internal/auth/*`）
- Key 校验链：格式 → 缓存/DB → status → expires_at → IP(CIDR) → 模型白名单 → 额度存在性。
- 缓存 TTL 用 `config.Auth.KeyCacheTTL`，变更时失效（提供 `Invalidate(keyID/lookupHash)`）。
- 管理员 JWT 签发/校验（`golang-jwt/jwt/v5`）。
- 内部 HMAC 中间件（用 `internal/pkg/sign`），含 nonce 与时钟窗，失败映射到 `docs/internal-api-v1.md` 的 40100/40101/40102。
- 测试：无效/过期/禁用/IP 不符/模型不符 分别命中错误码；签名正确/错/重放/超时。

### WS-SETTLE：额度服务与 Worker（`internal/quota/*`、`internal/worker/*`）
- 实现 `billing.Ledger`：`Prehold` / `Settle` / `Credit` / `Balance`。
- 预扣走 `cache.Hold`（scope 见 `internal/cache/cache.go`），结算走 `cache.Release` + `store.Subscriptions.AddUsage`。
- 封顶：每 Key 日上限、每账号月上限、全站日上限（`config.Limits`），超限返回 `errs.InsufficientQuota()`。
- Worker：消费 `queue.TopicUsage` → `store.Usage.Insert` + `Aggregates.UpsertDaily`；队列不可用时同步落库。
- 测试：预扣→结算差额退补、并发预扣不超卖、封顶触发、Worker 幂等（同 request_id 重复投递只写一行）。

### WS-API：HTTP 层（`internal/api/*`）
- 三组路由：`/v1/*`（OpenAI 兼容）、`/internal/v1/*`（签名）、`/admin/v1/*`（JWT）。
- `/v1` 错误体严格 OpenAI 风格（附录 B），响应头带 `X-CZ-Request-Id`。
- `/internal/v1` 严格按 `docs/internal-api-v1.md`，**一个字段都不要偏**。
- 流式端点把 `relay.Forwarder` 的输出直接写 `http.ResponseWriter`。
- 测试：契约字段快照、错误码映射、SSE 透传。

### WS-SQL：生产驱动（`internal/store/mysqlstore`、`cache/rediscache`、`queue/redisqueue`、`internal/migrate`）
- 与内存驱动**语义等价**（幂等靠唯一索引 + `INSERT ... ON DUPLICATE KEY`）。
- `internal/migrate`：读 `migrations/*.sql`，按 `-- 0001_up` / `-- 0001_down` 分段执行，记录 `schema_migrations` 表，支持 `Up()` / `Down(n)` / `Status()`。
- 本地无 MySQL/Redis，因此**不要求跑通**，但必须：`go build` 通过、SQL 语句有针对性单测（语句生成层可测）、文档写明未验证项。

### WS-WEB：Vue3 前端（`web/`）
- Vue3 + TS + Vite + Pinia + Vue Router + Element Plus + ECharts。
- 页面：用户中心（概览/Key 管理/模型/用量/文档）、管理后台（仪表盘/渠道/模型/用户/订阅/财务/系统）。
- 构建产物输出到 `internal/api/console/dist`（`build.outDir`），并 `go:embed` 由 WS-WIRE 接入。
- **必须能 `pnpm build` 成功**；无后端时用 mock 数据，界面可独立预览。

### WS-PHP：V10 侧（`v10/`）
- 严格依 `docs/v10-notes.md` 与 `plan.md` §8–§10；目录/类名/命名空间/表名一字不差。
- 三个交付物：插件 `idcsmart_llm_gateway`、模块 `llm_gateway_module`、支付网关 `czapi_epay`。
- `model/LlmApi.php` 的签名实现必须与 `docs/internal-api-v1.md` §3 完全一致。
- 所有文件 **UTF-8 无 BOM**；`php -l` 全部通过。
