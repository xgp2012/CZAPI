# CZAPI —— 面向魔方业务系统 V10 的 LLM 网关（Go + Vue3）实施计划

> 版本：v0.3（规划稿：已吸收 Q1–Q4 答复、"有限测试直发生产"策略与官方插件源码实测结论）
> 日期：2026-11-04
> 配套文件：[docs/v10-notes.md](docs/v10-notes.md)（基于官方 idcsmart_ticket 4.2.2 源码的实测笔记，**与文档冲突时以该文件为准**）
> Q3/Q4 计费与模型适配参考 new-api，必要时借鉴其代码（许可证注意项见 §6.6 末注）。

---

## 0. 需求答复落定后的重大调整（先读这节）

| 你的答复 | 落地方式 |
| --- | --- |
| **Q1 收款在 V10 内部处理** | 网关不再自建钱包、不接支付回调、不做卡密、不直连易支付。**网关只剩"额度账本"**。充值、套餐、卡密、退款、开票全部走 V10 |
| **Q2 先最小限度测试** | P0 内的"最小穿透插件"（1.5–2 人日，见 docs/v10-notes.md §9 的 V1–V5），把基类/中间件/模板/主题问题在真实实例上打穿 |
| **Q3/Q4 参考 new-api** | 计量 = token × 模型倍率 × 分组倍率（借鉴 ratio 模型）；上游 = OpenAI 兼容 adapter + 模型映射表。**许可证口径待核实，只借鉴思路与接口形状，不复制源码**（§6.6 末注） |
| **有限测试直发生产** ★ | 测试砍到 4 人日必做项（§13.1），用**五道生产保险**替代长周期测试（§15.2）：功能开关、额度封顶、影子双跑、一键回滚、值班告警。排期 12 周 → **约 7 周至生产** |

由此带来的结构变化：
- 删除：网关侧 `cz_wallet_txn`、`cz_pay_order`、`cz_redeem_code`、`/pay/*` 回调、易支付对接、网关充值页。
- 新增：V10 支付网关插件（`public/plugins/gateway/czapi_epay/`）、V10 插件内卡密表、`/internal/v1/credit`（V10 → 网关加/扣额度）。
- 网关的"余额"与"套餐额度"统一成两类 **credit 账本**，由 V10 事件驱动变更。

### 0.1 旧方案 → 新方案对照

根据确认：**Q1 → 全部收款在 V10 内部处理；Q3/Q4 → 计费与上游适配参考 new-api。**
因此第一版计划中的"网关自建钱包 + 易支付直连 + 卡密体系"整体取消，改为：

| 旧方案 | 新方案 |
| --- | --- |
| 网关自收充值款（易支付直连回调） | ❌ 取消。网关不接支付回调，钱不经过网关 |
| 网关自建钱包 + 充值页 | ✅ 保留"余额"概念，但余额由 V10 侧驱动（V10 入账事件 → 网关加额度） |
| 卡密在网关生成/兑换 | ✅ 移到 V10 插件内做，兑换后走 V10 加款，再通知网关 |
| 网关实现易支付网关 | ✅ 改为写 V10 支付网关插件（`public/plugins/gateway/`），复用 V10 支付流程 |

**好处**：两端账务不重复、开票/退款口径天然统一、不需要处理"网关直接收钱"的合规问题。
**代价**：多写一个 V10 支付网关插件；V10 侧需要一次"入账事件 → 通知网关加额度"的同步机制（带幂等与失败重试）。

---

## 1. 目标与范围

### 1.1 业务目标

1. **OpenAI 兼容的 LLM 统一网关**：一套 `/v1/chat/completions` 聚合多个上游（OpenAI 兼容为主：DeepSeek、通义、智谱、Kimi、vLLM、OneAPI 等），提供路由、重试、限流、计费、日志、审计。
2. **V10 为唯一用户与商品入口**：不另建一套用户体系，V10 客户（client）即网关租户；V10 商品（host/product）即套餐；订单支付即收款。
3. **V10 插件（addon）**：用户在 V10 前台控制台完成 Key 创建/查看/禁用/删除、模型列表浏览、用量与额度查看；管理员在 V10 后台看渠道状态、客户用量、封禁 Key、同步模型。
4. **V10 模块（server/module）**：把「LLM 套餐」做成可售卖商品，下单自动开通（发 Key + 发额度）、到期暂停、续费恢复、删除回收。
5. **额度账本（new-api 风格计量）**：订阅套餐额度 + 余额额度两类 credit，全部由 V10 事件驱动，网关只扣不加（管理员调账除外）。

### 1.2 本期不做（明确排除，避免范围蔓延）

- 不做模型微调 / 训练 / 向量库托管（仅代理推理）。
- 不做 V10 之外的独立注册与登录（用户身份完全来自 V10）。
- **不做网关侧收款**（易支付回调、支付订单、钱包充值全部划归 V10，见 §0）。
- 不做多区域多活（首期单区域，多副本即可）。
- 不做应用商店上架（放到稳定运营后的独立迭代）。
- 不做 Anthropic/Gemini 原生协议（二期按需，先只保留适配器接口位）。

---

## 2. 已确认的关键决策

| 决策项 | 结论 | 影响 |
| --- | --- | --- |
| 计费模型 | **参考 new-api：订阅套餐 + 余额并存；按 token 计量 + 模型倍率 / 补全倍率 / 分组倍率** | 网关内置计量与倍率引擎；借鉴思路为主，许可证待核实（见 §6.6 末注） |
| **收款与退款** | **全部在 V10 内部处理**（V10 订单 + V10 支付网关 + 开票 + 退款）；**网关只做"额度账本"，不碰钱** | 网关不实现支付回调、不做收款方；钱包余额=由 V10 入账事件驱动的额度镜像 |
| 卡密 | 在 V10 插件内生成 / 兑换；兑换成功走 V10 加款或已支付订单，再通知网关加额度 | 卡密表落在 V10，网关只接"加额度"指令 |
| 易支付 | 写成 **V10 支付网关插件**（`public/plugins/gateway/`），跑在 V10 标准支付流程里 | 充值与套餐共用 V10 支付体系，开票退款统一，无需两端对账资金流 |
| 部署形态 | **网关独立服务器 + 独立域名**；V10 插件通过 HTTPS + 签名回调网关 API | 两端解耦；需要一套内部签名协议与容灾策略 |
| 插件范围 | Key 管理 + 模型列表 + 用量面板（MVP）；商品化模块；后台管理与同步 | 一期要做「插件 + 模块 + 后台」三块 PHP 侧能力 |
| 上游协议 | **OpenAI 兼容协议为主** | 只实现一套 adapter + 模型映射表，其余靠兼容层 |
| 用户体系 | **直接复用 V10 用户/客户体系**，网关不存密码、不做登录 | 网关侧 `account` 以 `v10_client_id` 为唯一键；V10 传 JWT 或签名换取网关 `access_token` |
| 规模 | 起步阶段，允许单机 Compose，后续平滑加节点 | 首版 Redis 限流 + 异步记账队列即可，不上独立计量服务 |

### 2.1 两种额度来源（全部由 V10 驱动）

| 形态 | 用户在哪买/兑换 | 钱去哪 | 额度从哪来 | 网关侧对象 |
| --- | --- | --- | --- | --- |
| **A. 套餐/订阅** | V10 商品下单（包月/包年/一次性） | V10（V10 支付网关，含易支付） | V10 模块开通 → 网关发放周期额度，周期重置 | `subscription` |
| **B. 余额（充值/卡密）** | V10 前台充值页（用 V10 支付）或 V10 插件内兑换卡密 | V10 | V10 入账成功事件 → 网关加余额额度 | `credit_ledger(type=balance)` |

> Key 的计费模式在创建时绑定：`billing_mode = subscription | balance | hybrid`。`hybrid` 表示"套餐额度用尽后自动改用余额"，对齐 new-api 用户体验，默认开启。
> **网关永远是"额度账本"而非"钱袋"**：所有加额度操作必须携带 V10 侧的幂等凭证（订单号/卡密号/调整单号），网关拒绝无凭证的加额。

---

## 3. 术语

| 术语 | 含义 |
| --- | --- |
| Gateway | 本项目的 Go 网关服务（CZAPI） |
| Plugin / Addon | 放在 V10 `public/plugins/addon/` 下的魔方插件（PHP） |
| Module | 放在 V10 `public/plugins/server/` 下的产品模块（PHP），负责商品开通/暂停/删除 |
| Host | V10 的"产品实例"，一个用户购买的一个套餐就是一条 host |
| Client | V10 的前台客户，对应网关的租户 |
| Channel | 网关到上游的通道（含供应商、base_url、密钥池、权重、可用模型） |
| Key | 用户调用 LLM 的 `sk-xxx`，只在 V10 里管理 |
| Account | 网关侧账户，1:1 绑定 `v10_client_id` |

---

## 4. 总体架构

### 4.1 组件图

```
                     ┌───────────────────────────── 魔方业务系统 V10 ─────────────────────────────┐
                     │ 前台控制台(console)                    后台(DIR_ADMIN)                      │
                     │   └─ 插件页面(Key/模型/用量/充值)        └─ 插件页(渠道/用量/封禁/卡密)      │
                     │        │ addon 控制器 (CheckHome)              │ addon 控制器 (CheckAdmin)   │
                     │        └────────────┬──────────────────────────┘                             │
                     │                     ▼                                                        │
                     │  插件 IdcsmartLlmGateway.php（+ hooks.php）                                  │
                     │  模块 LlmGatewayModule.php (createAccount/suspend/terminate/renew/get_host_flow)│
                     │  支付网关插件 czapi_epay（public/plugins/gateway/，易支付）★收款只在这里       │
                     │  MySQL: 插件表 + 模块表 + 卡密 + V10 原生订单/交易                            │
                     └─────────────────────┬────────────────────────────────────────────────────────┘
                                           │ HTTPS + HMAC-SHA256 签名（服务端到服务端，双向）
                                           ▼
   ┌───────────────────────────────────────────────────────────────────────────────────────────────┐
   │                              CZAPI Gateway (Go, 独立域名 api.xxx.com)                         │
   │                                                                                               │
   │  ┌──────────────┐   ┌───────────────┐   ┌──────────────────┐   ┌────────────────────────┐    │
   │  │ /v1/* 兼容API│   │ /admin 管理API │   │ /internal/*      │   │ /console (Vue3 SPA)    │    │
   │  │ (用户 Key)   │   │  (管理员 JWT)  │   │ (V10 插件签名)    │   │ 管理后台 + 用户中心 iframe│  │
   │  └──────┬───────┘   └───────┬───────┘   └────────┬─────────┘   └────────────────────────┘    │
   │         │                   │                    │  ★ 无 /pay 回调（钱不经过网关）            │
   │  ┌──────▼───────────────────▼────────────────────▼──────────────────────────────────────┐    │
   │  │        Core: 鉴权 → 额度预扣 → 路由选渠道 → 转发(流式) → 结算(倍率计价)              │    │
   │  └──────┬─────────────────────────┬──────────────────────────┬──────────────────────────┘    │
   │         │                         │                          │                              │
   │   ┌─────▼─────┐            ┌──────▼──────┐           ┌───────▼────────┐                     │
   │   │ MySQL 8   │            │  Redis 7    │           │ 异步记账 Worker │                     │
   │   │(账务/日志)│            │(限流/预扣/  │           │ (usage→扣额落库)│                     │
   │   └───────────┘            │ 缓存/nonce) │           └───────┬────────┘                     │
   │                            └─────────────┘                   │                              │
   └──────────────────────────────────────────────────────────────┼──────────────────────────────┘
                                                                  │
        ┌──────────────┬──────────────┬──────────────┬───────────┴────┐
        ▼              ▼              ▼              ▼                ▼
   OpenAI 兼容池   DeepSeek      通义/智谱/Kimi   自建 vLLM      （二期）Claude/Gemini
   (OneAPI 等)
```

### 4.2 职责边界（很重要，防止端上重复实现）

| 能力 | 归属 | 说明 |
| --- | --- | --- |
| 用户注册/登录/实名/风控 | **V10** | 网关不存密码，不做登录页 |
| 商品、订单、**收款/退款/开票（含易支付、卡密）** | **V10** | 套餐与充值全部走 V10 支付体系；网关不碰钱 |
| 余额/套餐额度账本 | **网关** | V10 入账事件驱动加额，推理消耗由网关扣减；双向对账，以网关为准 |
| Key 的生命周期与展示 | **V10 插件**（网关存储） | 明文只在创建时返回一次，V10 只存前缀 + 网关 key_id |
| 推理流量、模型路由、限流 | **网关** | V10 不转发流量 |
| 用量明细与账单聚合 | **网关**（明细）+ **V10**（展示/按量出账） | 双向对账，以网关为准 |

---

## 5. 关键业务流程

### 5.1 用户在 V10 创建 Key

```
用户                V10 插件 frontend        V10 插件后台(PHP)            网关 /internal
 │  点击"创建Key" ────────▶│                          │                          │
 │                        │ POST console/addon       │                          │
 │                        │ (_plugin=idcsmart_llm     │                          │
 │                        │  _controller=key          │                          │
 │                        │  _action=create)          │                          │
 │                        │─────────────────────────▶│ 1. get_client_id()        │
 │                        │                          │ 2. 查 host 是否有效/有额度 │
 │                        │                          │ 3. 组装签名请求 ─────────▶│ 4. 校验签名/时序/nonce
 │                        │                          │                          │ 5. 生成 sk-xxxx（随机32B）
 │                        │                          │                          │ 6. 存 sha256(key) + 前缀
 │                        │                          │                          │ 7. 绑定 account/sub/host
 │                        │                          │◀───────── 返回 key 明文(一次) │
 │                        │                          │ 8. 落 V10 插件表(key_id,prefix)
 │◀── 弹窗展示一次 + 复制 ──│◀─────────────────────────│                          │
```

要点：
- **Key 明文只出现在这一次响应里**，网关只存哈希，V10 只存 `key_id + prefix`。用户丢了只能重建。
- 一个 host 可创建多个 Key（可配置上限，默认 5），便于分环境使用。
- Key 名称、可用模型范围、过期时间、IP 白名单都由插件页面提交。

### 5.2 调用推理（流式）

```
Client ──Authorization: Bearer sk-xxx──▶ Gateway /v1/chat/completions
                                          │
                                          ├─1. key 鉴权（sha256 查缓存 → 命中即过，未命中查库回填）
                                          ├─2. 校验：key 状态/过期/IP白名单/模型白名单/scheme
                                          ├─3. 限流：RPM / TPM / 并发（Redis token bucket）
                                          ├─4. 配额预扣：估算 input + max_tokens → Redis 冻结
                                          ├─5. 选渠道：模型映射 → 剔熔断/满负载 → 加权随机
                                          ├─6. 转发（HTTP/1.1 或 h2，SSE 直通）
                                          │     └─ 5xx/超时：换渠道重试 ≤2；已下发首字节后不重试
                                          ├─7. 流结束：解析 usage（stream_options.include_usage）
                                          ├─8. 结算：实际费用 vs 冻结额，差额退回/补扣
                                          └─9. 投递 usage 事件到异步队列（明细落库 + 聚合）
```

### 5.3 套餐开通 / 续费 / 到期（V10 模块）

```
V10 订单支付成功 (hook: order_paid / 模块 createAccount)
   → 模块 LlmGatewayModule::createAccount(host, client, product, server)
   → 调网关 /internal/v1/subscriptions（携带 host_id, client_id, plan_id, period）
   → 网关建 subscription + 发放额度 + 把该 host 下的 Key 置为 active
   → 失败时返回 status=400 让 V10 订单进入"待人工/开通失败"队列（不要静默成功）

到期/欠费：V10 到期暂停 → suspendAccount → 网关把 Key 置 suspended（返回 402/403 提示）
续费：renew → 网关延长周期 + 重置周期额度（按 prorate 规则可配）
删除：terminateAccount → 网关吊销 Key + 归档用量
按量补充：hook get_host_flow（每周期）→ 返回该 host 期间消费金额/用量 → 交给 V10 按量出账
```

### 5.4 充值 / 卡密（全部在 V10 内完成）

```
【余额充值】用户 → V10 前台充值页（插件页面）
   1. 插件按金额生成 V10 充值单据（走 V10 订单体系，订单类型标记为"余额充值"）
   2. 用户选支付方式 → V10 收银台 → 支付网关（含我们的 czapi_epay 易支付网关插件）
   3. V10 侧支付成功 → V10 入账（交易流水落在 V10，开票/退款由 V10 负责）
   4. 插件 hook（order_paid 或 V10 交易回调）→ 调网关 POST /internal/v1/credit
      body: { v10_client_id, kind: "balance", amount, ref_type: "v10_order", ref_id: 订单号 }
   5. 网关幂等加额（唯一索引 (ref_type, ref_id)）→ 返回最新余额
   6. 网关加额失败：进 V10 插件重试队列，页面提示"到账处理中"

【卡密】管理员在插件后台批量生成 → 用户在插件页兑换
   1. 生成：code = 20 位 base32（去易混淆字符），库中只存 sha256 + 前缀 + 批次号
   2. 兑换：校验存在/未用/未过期/批次未禁用 → 标记已用（事务内 UPDATE ... WHERE redeemed_at IS NULL）
   3. 兑换成功 → 同等第 4 步，ref_type="redeem_code", ref_id=卡密 ID
   4. 失败回滚卡密状态；每人每日 5 次失败上限防爆破

【管理员调账】插件后台"客户用量"页 → 调 /internal/v1/credit（amount 可为负，ref_type="admin_adjust"）
```

> **网关的硬性规则**：`/internal/v1/credit` 必须带 `ref_type + ref_id`，且 `(ref_type, ref_id)` 唯一。任何无凭证的加额请求一律 400。这样即使 V10 重复通知、重试、人工补单，也不会多给额度。
> 退款同理：V10 退款 → 调 `/internal/v1/credit` 带**负数**金额与退款单号，网关扣回（若余额不足则记负额度 `allow_negative`，下次充值抵扣）。

---

## 6. 网关（Go）技术设计

### 6.1 技术选型

| 层 | 选型 | 理由 |
| --- | --- | --- |
| 语言 | Go 1.23+ | 高并发 SSE 转发、内存占用低 |
| HTTP 框架 | `chi` 或 `gin` + 标准库 `net/http` | 流式转发需要 `http.Flusher` 直控；不引入重框架 |
| 配置 | `viper`（yaml + env） | 便于容器化 |
| ORM | `sqlc`（生成类型安全查询）+ `goose` 迁移 | 账务 SQL 需要可控；避免 ORM 隐式行为 |
| 数据库 | MySQL 8.0（与 V10 同栈，运维熟悉） | JSON 字段、分区表；PG 作为备选（JSONB 更优） |
| 缓存/限流 | Redis 7（`redis/go-redis/v9`） | 令牌桶、预扣、nonce、渠道健康度 |
| 队列 | Redis Stream（首期）→ NATS/Kafka（量大再换） | 记账必须异步、可重放；接口先抽象 |
| 鉴权 | 自建（Key→sha256）、管理员 JWT、内部 HMAC | 无外部依赖 |
| 观测 | Prometheus + Grafana、`slog` 结构化日志、OpenTelemetry（可选） | |
| 前端 | Vue3 + TS + Vite + Pinia + Vue Router + Element Plus（或 Arco） + ECharts | 管理后台 + 用户中心 |
| 部署 | Docker Compose（首期）/ K8s（后期）、Nginx 反代 | |

### 6.2 仓库结构（monorepo）

```
czapi/
├── cmd/
│   ├── server/main.go            # HTTP 网关
│   └── worker/main.go            # 异步记账/聚合/对账
├── internal/
│   ├── api/                      # 路由注册
│   │   ├── openai/               # /v1/* 兼容层（请求/响应/SSE）
│   │   ├── admin/                # /admin/v1/*
│   │   ├── internal/             # /internal/v1/*（V10 插件专用，签名）
│   │   └── console/              # Vue3 静态资源
│   ├── auth/                     # key 鉴权、admin jwt、HMAC 签名校验
│   ├── billing/                  # 计量、倍率定价、预扣/结算、订阅、余额账本
│   ├── relay/                    # 渠道选择、转发、重试、熔断、SSE
│   ├── provider/                 # adapter：openai / (二期 anthropic/gemini)
│   ├── quota/                    # Redis 原子额度（预扣/回填）
│   ├── ratelimit/                # rpm/tpm/并发
│   ├── account/                  # 账户与 V10 映射
│   ├── model/                    # 模型目录、映射、分组
│   ├── store/                    # sqlc 生成 + 仓储
│   ├── worker/                   # usage 聚合、对账
│   └── pkg/{log,errs,sign,crypto,idgen,clock}
├── migrations/
├── web/                          # Vue3（admin + console）
│   ├── src/{views,components,stores,api,router,i18n}
│   └── vite.config.ts            # build → internal/api/console/dist
├── deploy/{docker-compose.yml,Caddyfile,grafana/}
├── docs/{v10-notes.md,sign.md,runbook.md,api.md}
└── v10/                          # ★ PHP 侧交付物（按官方安装包形式发布）
    ├── idcsmart_llm_gateway/     # 插件：public/plugins/addon/
    │   ├── IdcsmartLlmGateway.php        # extends app\common\lib\Plugin
    │   ├── hooks.php                     # add_hook 注册（可选，也可用主类小驼峰）
    │   ├── route.php                     # console/v1、console/v1/api(Check)、{DIR_ADMIN}/v1
    │   ├── sidebar_clientarea.php        # 前台导航
    │   ├── sidebar.php                   # 后台导航
    │   ├── auth.php / auth_clientarea.php # 后台/前台权限点
    │   ├── config.php                    # 插件配置表单（网关地址、app_id、secret）
    │   ├── upgrade.php
    │   ├── controller/{IndexController,KeyController,ChannelController}.php
    │   ├── controller/clientarea/{IndexController,KeyController,ModelController}.php
    │   ├── model/LlmApi.php              # 网关客户端（签名/重试/超时）
    │   ├── logic/、validate/
    │   ├── lang/{zh-cn,zh-hk,en-us}.php
    │   └── template/
    │       ├── admin/{index.html,key.html,channel.html,js/*,css/*,lang/*.js}
    │       └── clientarea/pc/{default,mf801,mf_pc_701}/llm.html
    │           clientarea/mobile/{mfm201,mfm301}/llm.html   ← 每套主题一个 iframe 壳
    ├── llm_gateway_module/       # 模块：public/plugins/server/（商品化）
    │   ├── LlmGatewayModule.php
    │   ├── hooks.php             # get_host_flow
    │   ├── controller/{home,admin}/…
    │   └── lang/…
    └── czapi_epay/               # 支付网关插件：public/plugins/gateway/（易支付）
        ├── CzapiEpay.php
        └── controller/…
```

### 6.3 对外 API 清单

#### A. OpenAI 兼容层（用户 Key 鉴权）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/v1/chat/completions` | 核心，支持 stream / tools / vision |
| POST | `/v1/completions` | 兼容旧接口（可转发到 chat 或上游同路径） |
| POST | `/v1/embeddings` | 向量化 |
| POST | `/v1/responses` | 新协议（二期，视上游支持） |
| GET | `/v1/models` | **返回该 Key 可用模型**（来自 V10 套餐授权） |
| GET | `/v1/models/{id}` | 单模型详情 |
| POST | `/v1/moderations` | 可选 |
| GET | `/v1/usage` | 扩展：本 Key 当日/当月用量（网关自定义，方便 SDK 对接） |

请求头约定：`Authorization: Bearer sk-...`、`X-Request-Id`（可选，覆盖网关生成）、`X-CZ-Channel`（可选，管理员调试用）。

错误体统一 OpenAI 风格：`{"error":{"message","type","param","code"}}`，见附录 B。

#### B. 内部接口（V10 插件调用，HMAC 签名）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/internal/v1/accounts/sync` | 创建/更新账户（client_id、邮箱快照） |
| POST | `/internal/v1/credit` | **加/扣余额额度**（amount 可负；必带 ref_type+ref_id 幂等；来源：V10 充值单/卡密兑换/管理员调账/退款） |
| POST | `/internal/v1/keys` | 创建 Key（返回明文一次） |
| GET | `/internal/v1/keys` | 列出某 client 的 Key（含用量、状态） |
| PATCH | `/internal/v1/keys/{id}` | 改名/改模型范围/改IP白名单/启停 |
| DELETE | `/internal/v1/keys/{id}` | 吊销 |
| GET | `/internal/v1/accounts/{client_id}/quota` | 额度与到期时间（套餐+余额） |
| GET | `/internal/v1/models` | 可售模型目录（含分组、倍率、上下文长度） |
| GET | `/internal/v1/usage/summary` | 按 client/host/日期聚合用量 |
| GET | `/internal/v1/usage/logs` | 明细分页（脱敏：不返回 prompt 内容） |
| POST | `/internal/v1/subscriptions` | 开通/续费套餐（host_id 维度，幂等键 = host_id+period） |
| POST | `/internal/v1/subscriptions/{id}/suspend` | 暂停（到期/欠费） |
| POST | `/internal/v1/subscriptions/{id}/resume` | 恢复 |
| DELETE | `/internal/v1/subscriptions/{id}` | 终止回收 |
| GET | `/internal/v1/health` | 连通性探测（模块 testConnect 用） |
| GET | `/internal/v1/billing/host-usage` | 按量计费取数（对应 hook `get_host_flow`） |

> 已删除（对比 v0.1）：`/pay/epay/*` 支付回调 —— 收款不经过网关。

#### C. 管理接口（`/admin/v1/*`，管理员 JWT）

渠道 CRUD、密钥池轮换、模型目录/映射、定价（per-1M tokens、倍率）、用户与 Key 检索封禁、用量与收入看板、卡密批次、订单/对账、审计日志、系统设置（限流默认值、熔断阈值、日志保留期）。

#### D. 支付回调

**已取消**（v0.2）：收款不经过网关，无 `/pay/*`。易支付作为 V10 支付网关插件在 V10 侧实现。

### 6.4 鉴权与配额

- **Key 存储**：`sk-` + 32 字节随机 base62；库中存 `sha256(key)` 加盐 + `key_prefix`（前 8 位用于列表展示）。校验走 Redis 缓存（TTL 60s，变更时主动失效）。
- **鉴权链**：格式校验 → 缓存/DB → `status=active` → `expires_at` → IP 白名单（CIDR）→ 模型白名单 → 订阅/余额额度 > 0。
- **配额预扣**：`estimate = prompt_tokens + max_tokens(默认值)`，按输入/输出分别计价后取总额，Redis `DECRBY` 到 `quota_frozen:{key_id}`；结算时按实际 usage 计算，多退少补，全程一个 Redis Lua 脚本保证原子性。
- **并发与速率**：三层桶 —— 每分钟请求数（RPM）、每分钟 token 数（TPM，按预估值计入，结算后校正）、并发连接数（Redis 计数器 + 超时自动回收）。

### 6.5 渠道与路由（结构参考 new-api 的 channel/relay 概念）

- **Channel**：`name, provider(openai), base_url, key_pool[], models[], weight, priority, timeout, max_concurrency, status`。
- **模型映射**：对外模型名（如 `gpt-4o-mini`、`deepseek-chat`）→ 渠道内模型名（如 `deepseek-v3`），支持 1:N 与别名（对齐 new-api 的 model mapping）。
- **选择算法**：按 `priority` 分层 → 层内按 `weight` 加权随机 → 过滤熔断/并发已满 → 记录选中的 key（密钥池轮询 + 失败冷却）。
- **重试策略**：连接错误/超时/5xx 换渠道重试 ≤2；4xx（除 429）直接返回；429 可切渠道一次；**流式已输出内容后不再重试**（避免重复计费与乱序）。
- **熔断**：滑动窗口 5 分钟内错误率 > 50% 且样本 > 20，摘除 60s，半开探测恢复。
- **超时**：连接 10s / 首字节 60s / 非流式总 300s / 流式两 chunk 间隔 120s。

### 6.6 计费引擎（对齐 new-api 的 ratio 体系）

new-api 的公式：`消耗额度 = (prompt_tokens + completion_tokens × completion_ratio) × model_ratio × group_ratio`，再按固定兑换率折算成货币。我们采用同一形状：

```
cost_tokens = (prompt + completion × completion_ratio(model)) × model_ratio(model) × group_ratio(用户分组)
cost_micros = ceil(cost_tokens × token_to_micros)          -- token→微元兑换率，全局配置
```

| 项 | 来源 | 说明 |
| --- | --- | --- |
| `model_ratio` | 网关 `cz_model` 表 | 相对基准价的倍率；可按模型 维护，借鉴 new-api 的"按模型倍率 + 按补全倍率"两层 |
| `completion_ratio` | `cz_model` 表 | 输出 token 相对输入的倍率（如 3.0），不同模型不同 |
| `group_ratio` | `cz_plan` / 用户分组 | 套餐等级折扣（如 basic 1.0 / pro 0.8） |
| `cached_tokens` | 上游 usage | 命中缓存的输入 token 按 `cached_ratio`（默认 0.5）打折 |
| 兑换率 `token_to_micros` | 系统配置 | 让"额度"与 V10 侧人民币定价对得上（V10 商品卖多少钱 → 换多少额度，由插件在开通时换算） |

- **计量**：优先取上游 `usage`；缺失时用 tokenizer 估算并标注 `estimated=true`；流式必须请求 `stream_options.include_usage=true`，并对上游不支持的做尾部拼接兜底。
- **计费模式**：`subscription`（扣周期额度）、`balance`（扣余额额度）、`hybrid`（先额度后余额）。
- **幂等**：每次请求生成 `request_id`（ULID），`usage_record` 唯一索引 `(request_id)`；重放不重复扣费。
- **异步**：转发完成后投递 `usage_event` 到 Redis Stream，Worker 消费后写 `usage_record` + 更新聚合 + 写 `credit_ledger` 扣减流水。Redis 不可用时降级为同步写（保证不丢账，宁可慢）。
- **对账**：每日 02:00 跑 `usage_record` 按 key/host 聚合 vs 订阅额度消耗 vs 余额扣减，三条链路必须相等，差异告警；同时核对网关 `credit_ledger` 与 V10 侧充值/卡密/调账单据（金额守恒）。

> **与 new-api 的许可证边界（⚠️ 待核实）**：本次环境无法访问 GitHub，未能读到 new-api 仓库的实际 LICENSE 文件，因此**下文协议名为待核实项**。业界常见情况是一套 one-api 衍生项目采用 AGPL-3.0、one-api 本体为 MIT，但**必须以你方拉取的仓库根目录 `LICENSE` 文件为准**（开工前 5 分钟即可确认）。
> 无论哪种协议，本项目的做法都成立：借鉴**计费公式、数据模型形状、路由概念**（属于思路，不受版权保护），**不复制其 Go 源码**；token 估算等通用逻辑使用独立 MIT 库（如 `tiktoken-go`）。若确认是 AGPL 而你方打算闭源商业交付，则这条边界是硬性的，不可让步。

### 6.7 流式（SSE）实现要点

- 上游 `Content-Type: text/event-stream` 直通，逐 `chunk` 读 → 立即 `Flush()`，客户端断开用 `r.Context().Done()` 取消上游请求（并触发一次结算，按已输出部分计费或按预扣全额，策略可配，默认按已收到 usage 结算，无 usage 则按预扣额结算）。
- 透传 `[DONE]`；不修改 client 期望的 data 结构（除了注入 `usage` chunk 的可选开关）。
- 出网连接池：`Transport.MaxIdleConnsPerHost` 调高（默认 200），禁用 `ResponseHeaderTimeout` 干扰流式。

### 6.8 多租户与 V10 映射

- 表 `account`：`id, v10_client_id(UNIQUE with site), v10_site_id, email_snapshot, status, balance_credit, created_at`。
- 表 `subscription`：`id, account_id, v10_host_id(UNIQUE), plan_id, period_start, period_end, quota_total, quota_used, status`。
- 一个 client 可有多个 host（多个套餐），一个 host 多个 key；额度按 host 分配、按 key 消耗，Key 可指定从哪个 subscription 扣（默认取最早到期）。
- **站点维度**：网关可服务多个 V10 站点，用 `v10_site_id`（即插件配置里的 app_id）隔离，V10 侧的"client_id"仅在同站点内唯一。

### 6.9 观测

- Metrics：`requests_total{model,channel,status}`、`ttft_seconds`、`tokens_total{type}`、`quota_rejected_total`、`channel_errors_total`、`billing_lag_seconds`、`worker_queue_depth`。
- 日志：结构化 JSON，`request_id` 贯穿；**默认不落 prompt/response 正文**（合规），可开关"脱敏留存 7 天"。
- 告警：渠道错误率、计费延迟 > 60s、余额负数、对账差异、V10 通知网关加额失败堆积。

---

## 7. 数据模型（MySQL 8，核心表）

> 命名：`cz_` 前缀。金额用 `DECIMAL(20,8)` 或"微元整数 BIGINT"（推荐后者，避免浮点误差，1 元 = 1,000,000 微元）。额度用 `BIGINT` 表示 token 数。

```sql
-- 账户（对应 V10 client）
CREATE TABLE cz_account (
  id             BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  v10_site_id    VARCHAR(64)  NOT NULL,
  v10_client_id  BIGINT UNSIGNED NOT NULL,
  email          VARCHAR(190) NULL,
  status         TINYINT NOT NULL DEFAULT 1,          -- 1正常 0禁用
  balance_credit BIGINT NOT NULL DEFAULT 0,           -- 余额额度（微元），由 V10 入账事件增减
  allow_negative TINYINT NOT NULL DEFAULT 0,          -- 退款导致的负额度是否允许
  created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  UNIQUE KEY uk_site_client (v10_site_id, v10_client_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 套餐定义（由管理员维护，V10 商品通过 plan_code 关联）
CREATE TABLE cz_plan (
  id            BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  plan_code     VARCHAR(64) NOT NULL,                 -- 与 V10 商品配置一致
  name          VARCHAR(128) NOT NULL,
  quota_tokens  BIGINT NOT NULL,                      -- 每周期额度
  period        ENUM('month','quarter','year','once') NOT NULL DEFAULT 'month',
  rpm_limit     INT NOT NULL DEFAULT 60,
  tpm_limit     INT NOT NULL DEFAULT 100000,
  concurrency   INT NOT NULL DEFAULT 5,
  model_group   VARCHAR(64) NOT NULL DEFAULT 'default',
  max_keys      INT NOT NULL DEFAULT 5,
  price_cents   BIGINT NOT NULL DEFAULT 0,
  status        TINYINT NOT NULL DEFAULT 1,
  UNIQUE KEY uk_plan_code (plan_code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 订阅（= V10 host）
CREATE TABLE cz_subscription (
  id           BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  account_id   BIGINT UNSIGNED NOT NULL,
  v10_host_id  BIGINT UNSIGNED NOT NULL,
  plan_id      BIGINT UNSIGNED NOT NULL,
  period_start DATETIME NOT NULL,
  period_end   DATETIME NOT NULL,
  quota_total  BIGINT NOT NULL,
  quota_used   BIGINT NOT NULL DEFAULT 0,
  billing_mode ENUM('subscription','balance','hybrid') NOT NULL DEFAULT 'hybrid',
  status       ENUM('active','suspended','expired','terminated') NOT NULL DEFAULT 'active',
  created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uk_host (v10_host_id),
  KEY idx_account (account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- API Key（只存哈希）
CREATE TABLE cz_api_key (
  id            BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  account_id    BIGINT UNSIGNED NOT NULL,
  subscription_id BIGINT UNSIGNED NULL,
  key_hash      CHAR(64) NOT NULL,                    -- sha256(salt||key)
  key_prefix    VARCHAR(20) NOT NULL,                 -- sk-abcd… 用于展示
  name          VARCHAR(128) NOT NULL DEFAULT '',
  models        JSON NULL,                            -- null = 跟随套餐
  ip_whitelist  JSON NULL,                            -- ["1.2.3.0/24"]
  expires_at    DATETIME NULL,
  status        ENUM('active','suspended','revoked') NOT NULL DEFAULT 'active',
  last_used_at  DATETIME NULL,
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uk_hash (key_hash),
  KEY idx_account (account_id),
  KEY idx_sub (subscription_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 渠道与密钥池
CREATE TABLE cz_channel (
  id          BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  name        VARCHAR(128) NOT NULL,
  provider    VARCHAR(32) NOT NULL DEFAULT 'openai',
  base_url    VARCHAR(255) NOT NULL,
  key_pool    JSON NOT NULL,                          -- [{key_enc, weight, status}]
  models      JSON NOT NULL,                          -- ["deepseek-chat", ...]
  model_map   JSON NULL,                              -- {"gpt-4o-mini":"deepseek-chat"}
  weight      INT NOT NULL DEFAULT 100,
  priority    INT NOT NULL DEFAULT 0,
  max_concurrency INT NOT NULL DEFAULT 100,
  timeout_ms  INT NOT NULL DEFAULT 300000,
  status      TINYINT NOT NULL DEFAULT 1,
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 用量明细（大表，按月分区）
CREATE TABLE cz_usage_record (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  request_id    CHAR(26) NOT NULL,
  account_id    BIGINT UNSIGNED NOT NULL,
  subscription_id BIGINT UNSIGNED NULL,
  api_key_id    BIGINT UNSIGNED NOT NULL,
  v10_host_id   BIGINT UNSIGNED NULL,
  model         VARCHAR(128) NOT NULL,
  channel_id    BIGINT UNSIGNED NOT NULL,
  prompt_tokens INT NOT NULL DEFAULT 0,
  completion_tokens INT NOT NULL DEFAULT 0,
  cached_tokens INT NOT NULL DEFAULT 0,
  cost_micros   BIGINT NOT NULL DEFAULT 0,
  billing_mode  TINYINT NOT NULL,                     -- 1订阅额度 2余额额度
  estimated     TINYINT NOT NULL DEFAULT 0,
  stream        TINYINT NOT NULL DEFAULT 0,
  latency_ms    INT NOT NULL DEFAULT 0,
  ttft_ms       INT NOT NULL DEFAULT 0,
  status_code   SMALLINT NOT NULL,
  error_code    VARCHAR(64) NULL,
  created_at    DATETIME NOT NULL,
  PRIMARY KEY (id, created_at),
  UNIQUE KEY uk_request (request_id, created_at),
  KEY idx_key_time (api_key_id, created_at),
  KEY idx_host_time (v10_host_id, created_at),
  KEY idx_account_time (account_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
PARTITION BY RANGE COLUMNS(created_at) (/* 每月一分区，保留 6~12 个月 */);

-- 余额额度流水（不可变账本；只由 V10 凭证驱动）
CREATE TABLE cz_credit_ledger (
  id            BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  account_id    BIGINT UNSIGNED NOT NULL,
  type          ENUM('v10_order','redeem_code','admin_adjust','refund','consume') NOT NULL,
  amount_micros BIGINT NOT NULL,                      -- 正数入账，负数出账
  balance_after BIGINT NOT NULL,
  ref_type      VARCHAR(32) NOT NULL,                 -- v10_order / redeem_code / admin_adjust
  ref_id        VARCHAR(64) NOT NULL,                 -- ★ 幂等凭证：订单号/卡密ID/调整单号
  remark        VARCHAR(255) NULL,
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  KEY idx_account_time (account_id, created_at),
  UNIQUE KEY uk_ref (ref_type, ref_id)                -- 同一凭证只入账一次
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 说明：支付订单、卡密本体、退款单据全部在 V10 侧（V10 订单/交易 + 插件表），网关不存。

-- 模型目录 / 倍率定价（new-api 风格）
CREATE TABLE cz_model (
  id               BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  model_name       VARCHAR(128) NOT NULL,
  display_name     VARCHAR(128) NOT NULL,
  group_name       VARCHAR(64) NOT NULL DEFAULT 'default',
  context_len      INT NOT NULL DEFAULT 0,
  model_ratio      DECIMAL(10,4) NOT NULL DEFAULT 1.0,   -- 相对基准倍率
  completion_ratio DECIMAL(10,4) NOT NULL DEFAULT 1.0,   -- 输出相对输入倍率
  cached_ratio     DECIMAL(10,4) NOT NULL DEFAULT 0.5,   -- 缓存命中输入折扣
  capabilities     JSON NULL,                             -- ["vision","tools","json"]
  status           TINYINT NOT NULL DEFAULT 1,
  UNIQUE KEY uk_model (model_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 全局计费配置（也可用 key-value 配置表）
-- token_to_micros: 1 token 标准倍率下的微元值；token_to_micros = 1 元 / 1_000_000 标准 token 等

-- 内部调用签名 nonce（也可只用 Redis）
CREATE TABLE cz_nonce (
  nonce      VARCHAR(64) PRIMARY KEY,
  app_id     VARCHAR(64) NOT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

V10 侧新增表（插件 `install()` 创建；命名规则参照官方插件 `idcsmart_addon_{插件名}_*`）：

```sql
idcsmart_addon_idcsmart_llm_gateway_key       -- key_id, client_id, host_id, name, prefix, status, models, created_at
idcsmart_addon_idcsmart_llm_gateway_redeem    -- 卡密：batch_no, code_hash, code_prefix, value_micros, redeemed_by, redeemed_at, expires_at
idcsmart_addon_idcsmart_llm_gateway_txn       -- 充值/卡密/调账单据：client_id, kind, amount_micros, status, ref_no, created_at
idcsmart_addon_idcsmart_llm_gateway_pushlog   -- 通知网关的失败重试队列：endpoint, payload, retry_count, next_retry_at, status
idcsmart_addon_idcsmart_llm_gateway_usage     -- 用量快照（按 key/day），减少对网关实时请求
```

> 配置项（网关地址、app_id、secret）**不建表**，直接放插件 `config.php`（V10 自动渲染与存储）。


---

## 8. 魔方 V10 插件设计（PHP）

> 本章所有结论来自官方 `idcsmart_ticket` 4.2.2 源码实测，详见 [docs/v10-notes.md](docs/v10-notes.md)。**与官方文档冲突处以源码为准。**

### 8.1 定位与目录

插件标识 `idcsmart_llm_gateway`，目录放 `public/plugins/addon/`，发布形式与官方插件一致（tar.gz，内层单个目录）：

```
idcsmart_llm_gateway/
├── IdcsmartLlmGateway.php        # 主类，extends app\common\lib\Plugin
├── hooks.php                     # add_hook() 注册（也可用主类小驼峰方法，二选一或混用）
├── route.php                     # 自定义路由：console/v1、console/v1/api(免登录)、{DIR_ADMIN}/v1
├── sidebar.php                   # 后台导航（可选）
├── sidebar_clientarea.php        # ★ 前台导航（会员中心侧栏入口）
├── auth.php                      # 后台权限点（配合 v-permission）
├── auth_clientarea.php           # 前台权限点
├── config.php                    # ★ 插件配置表单，V10 后台自动渲染
├── upgrade.php                   # 版本升级钩子
├── controller/
│   ├── IndexController.php       # 后台仪表盘
│   ├── KeyController.php         # 后台 Key 检索/封禁
│   ├── ChannelController.php     # 后台渠道状态、模型同步
│   ├── RedeemController.php      # 后台卡密批次生成/导出
│   └── clientarea/
│       ├── IndexController.php   # 前台：概览（额度/用量）
│       ├── KeyController.php     # 前台：Key CRUD
│       ├── ModelController.php   # 前台：模型列表
│       └── RedeemController.php  # 前台：卡密兑换
├── model/LlmApi.php              # 网关客户端（HMAC 签名、重试、超时、失败入队）
├── model/*Model.php              # 各表模型（ThinkPHP Model 风格）
├── logic/                        # 业务逻辑层
├── validate/                     # 校验器
├── lang/{zh-cn.php,zh-hk.php,en-us.php}
└── template/
    ├── admin/
    │   ├── index.html            # 对应 plugin/{插件名}/index.html
    │   ├── key.html、channel.html、redeem.html
    │   ├── js/{index,key,channel,redeem}.js、js/common/(vue.js,tdesign.min.js,axios.min.js)
    │   ├── api/*.js、css/*、lang/{zh-cn,zh-hk,en-us}.js
    └── clientarea/
        ├── pc/{default,mf801,mf_pc_701}/llm.html        # ★ 每套 PC 主题一个 iframe 壳
        └── mobile/{mfm201,mfm301}/llm.html              # ★ 每套移动模板一个 iframe 壳
```

> 文件编码统一 **UTF-8 无 BOM**（已按字节验证官方插件同为 UTF-8）。

### 8.2 主类骨架

```php
namespace addon\idcsmart_llm_gateway;

use app\common\lib\Plugin;          // ★ 与官方插件一致

class IdcsmartLlmGateway extends Plugin
{
    public $info = [
        'name'        => 'IdcsmartLlmGateway',
        'title'       => 'LLM 网关',
        'description' => '在魔方业务系统内管理 LLM API Key、模型与用量',
        'author'      => 'your-company',
        'version'     => '1.0.0',
    ];

    public function install()
    {
        // 直接执行 CREATE TABLE（官方插件就是在 install() 里跑 SQL，无 install.sql 约定）
        // 表名：idcsmart_addon_idcsmart_llm_gateway_key / _redeem / _txn / _pushlog / _usage
    }

    public function uninstall()
    {
        // 默认保守：只删配置与导航，保留业务数据；另提供后台"彻底卸载"开关
    }
}
```

### 8.3 hook 注册（事件名需在目标实例上核对）

```php
// hooks.php
add_hook('order_paid', function ($param) {            // 支付成功：充值/套餐入账 → 通知网关加额度
    \addon\idcsmart_llm_gateway\model\LlmApi::onOrderPaid($param['id'] ?? 0);
});
add_hook('after_host_delete', function ($param) {     // 产品删除：吊销 Key
    \addon\idcsmart_llm_gateway\model\LlmApi::onHostDeleted($param);
});
add_hook('day_cron', function ($param) {              // 每日：用量快照、失败通知重试、到期提醒
    \addon\idcsmart_llm_gateway\model\LlmApi::dailySync();
});
```

> 官方工单插件**没有 hooks.php**，而是用主类小驼峰方法（`afterAdminDelete($param)`）承接 hook。两种方式都可用；本项目事件较多，统一用 `hooks.php` 便于集中管理。
> ⚠️ `day_cron` / `order_paid` 等事件名必须在目标实例的 165 个 Hook 文档中核对；官方插件里实证存在的事件名只有 `afterAdminDelete`。

### 8.4 控制器与路由（基类命名空间以源码为准）

```php
// controller/clientarea/KeyController.php
namespace addon\idcsmart_llm_gateway\controller\clientarea;

use app\event\controller\PluginBaseController;        // ★ 源码实测命名空间（文档写的 app\admin\... 是错的）

class KeyController extends PluginBaseController
{
    public function keyList()
    {
        $clientId = get_client_id();                  // 全局辅助函数
        $res = \addon\idcsmart_llm_gateway\model\LlmApi::listKeys($clientId);
        return json(['status' => 200, 'msg' => lang_plugins('success_message'), 'data' => $res]);
    }
}
```

后台控制器 `extends app\event\controller\PluginAdminBaseController`，用 `get_admin_id()` 取管理员 ID，审计用 `active_log($desc, 'addon_idcsmart_llm_gateway', $id)`。

```php
// route.php
use think\facade\Route;

// 免登录组：仅给"网关 → V10"的回调用（Check 只表示不校验登录态，安全性靠我们自己的 HMAC）
Route::group('console/v1/api', function () {
    Route::post('llm/callback/quota-exhausted', "\\addon\\idcsmart_llm_gateway\\controller\\clientarea\\KeyController@quotaExhausted")
        ->append(['_plugin'=>'idcsmart_llm_gateway','_controller'=>'key','_action'=>'quota_exhausted']);
})->middleware(\app\http\middleware\ParamFilter::class)
  ->middleware(\app\http\middleware\Check::class);

// 前台组：用户操作 Key / 模型 / 兑换
Route::group('console/v1', function () {
    Route::get('llm/keys',  "\\addon\\idcsmart_llm_gateway\\controller\\clientarea\\KeyController@keyList")
        ->append(['_plugin'=>'idcsmart_llm_gateway','_controller'=>'key','_action'=>'key_list']);
    Route::post('llm/keys', "\\addon\\idcsmart_llm_gateway\\controller\\clientarea\\KeyController@create")
        ->append(['_plugin'=>'idcsmart_llm_gateway','_controller'=>'key','_action'=>'create']);
    // ... 其余 CRUD
})->middleware(\app\http\middleware\ParamFilter::class)
  ->middleware(\app\http\middleware\CheckHome::class)
  ->middleware(\app\http\middleware\RejectRepeatRequest::class);

// 后台组
Route::group(DIR_ADMIN . '/v1', function () {
    Route::get('llm/dashboard', "\\addon\\idcsmart_llm_gateway\\controller\\IndexController@dashboard")
        ->append(['_plugin'=>'idcsmart_llm_gateway','_controller'=>'index','_action'=>'dashboard']);
})->middleware(\app\http\middleware\ParamFilter::class)
  ->middleware(\app\http\middleware\CheckAdmin::class);
```

| 中间件 | 含义 |
| --- | --- |
| `ParamFilter` | 参数过滤（三组都挂） |
| `Check` | **免登录**（不代表安全，需自行校验签名） |
| `CheckHome` | 前台登录态 |
| `CheckAdmin` | 后台管理员登录态 |
| `RejectRepeatRequest` | 防重复提交 |

### 8.5 前端方案（关键结论：V10 是 Vue2，我们无法直接挂 Vue3）

**源码事实**：插件页面是「HTML 片段 + 独立 JS 创建 Vue 实例」，前台全局是 **Vue2 + Element UI（`el-*`）**，后台是 **Vue2 + TDesign（`t-*`）**，`template/clientarea` 还要按**主题目录**（`pc/default`、`pc/mf801`、`pc/mf_pc_701`、`mobile/mfm201`、`mobile/mfm301`）各放一份。

| 方式 | 做法 | 结论 |
| --- | --- | --- |
| **A. iframe 嵌入网关 Vue3 控制台** | 每个主题目录放一个 10 行的 `llm.html`：全屏 iframe → `{gateway}/console/v10-sso?token=一次性票据`，网关校验后下发 HttpOnly cookie | ✅ **MVP 采用**。Vue3 只维护一份；不受 5 套主题影响；样式冲突为零 |
| **B. Vue3 iife 挂载到空 div** | Vue3 打成 iife，`Vue3.createApp().mount('#czapi')`，不要复用 V10 的 Vue2 实例 | 二期可选（上架商店前）。仍需按主题目录放页面，但可复用同一份 bundle |
| ❌ 复用 V10 的 Vue 实例 | 在 V10 页面里 `new Vue({...})` 写 Vue3 语法 | 不可行（Vue2 运行时，无 Composition API） |

`llm.html` 骨架（每套主题一份，内容相同）：

```html
<!-- 页面独有样式 -->
<link rel="stylesheet" href="/plugins/addon/idcsmart_llm_gateway/template/clientarea/pc/default/css/llm.css">
</head>
<body>
  <div class="template llm">
    <div id="mainLoading" class="ddr ddr1"></div>
    <iframe id="czapi-fr" src="<?= $ssoUrl ?>" style="width:100%;border:0;min-height:calc(100vh - 120px)"></iframe>
  </div>
  <script src="/plugins/addon/idcsmart_llm_gateway/template/clientarea/pc/default/js/llm.js"></script>
</body>
```

> 若目标站点主题不在上表内，需再加一个同名目录（`pc/{主题名}/llm.html`）。主题目录名可从站点前台静态资源路径判断，P0 阶段一次性确认。

### 8.6 插件配置（走 `config.php`，不自己写配置页）

```php
// config.php
return [
    'gateway_base_url' => ['title'=>'网关地址','type'=>'text','value'=>'https://api.example.com','tip'=>'不带结尾斜杠'],
    'app_id'           => ['title'=>'App ID','type'=>'text','value'=>'','tip'=>'由网关后台签发'],
    'app_secret'       => ['title'=>'App Secret','type'=>'password','value'=>'','tip'=>'仅显示尾4位'],
    'plan_map'         => ['title'=>'套餐映射','type'=>'textarea','value'=>'{}','tip'=>'JSON: {"商品ID":"plan_code"}'],
    'default_billing_mode' => ['title'=>'默认计费模式','type'=>'select','value'=>'hybrid','tip'=>'subscription|balance|hybrid'],
    'max_keys_per_host'=> ['title'=>'每产品Key上限','type'=>'text','value'=>5,'tip'=>''],
    'timeout_ms'       => ['title'=>'调用超时(ms)','type'=>'text','value'=>5000,'tip'=>''],
];
```

| 配置项 | 说明 |
| --- | --- |
| `gateway_base_url` | 网关地址 |
| `app_id` / `app_secret` | 网关按 V10 站点签发；支持双 secret 轮换 |
| `plan_map` | V10 商品 → 网关 `plan_code` 映射 |
| `default_billing_mode` | subscription / balance / hybrid |
| `max_keys_per_host` | 每产品可建 Key 数 |
| `timeout_ms` | 内部调用超时 |

### 8.7 内部签名协议（插件 ↔ 网关，S2S）

**请求头**

```
X-CZ-App-Id:    {app_id}
X-CZ-Timestamp: {unix秒}
X-CZ-Nonce:     {32位随机}
X-CZ-Signature: {hex 小写}
Content-Type:   application/json
```

**待签串**（`\n` 分隔，全部 ASCII）

```
{METHOD}
{PATH}                      # 含前缀，如 /internal/v1/keys
{SORTED_QUERY}              # key1=val1&key2=val2，按 key 升序，URL 编码一致
{SHA256_HEX_OF_BODY}        # 空体用空串的 sha256
{TIMESTAMP}
{NONCE}
```

**签名**：`hex(HMAC_SHA256(app_secret, 待签串))`

**校验规则（网关侧）**
1. `app_id` 存在且启用；
2. `|now - timestamp| ≤ 300s`；
3. `nonce` 在 Redis `SETNX ex 300` 成功（防重放）；
4. 重算签名，`subtle.ConstantTimeCompare` 比较；
5. 失败统一返回 `401 {"code":"sign_invalid"}`，并在审计日志记录（含来源 IP）。

**响应签名（建议）**：`X-CZ-Sign-Server: hex(HMAC(secret, sha256(body)+timestamp))`，插件可选校验，防止中间人篡改。

**密钥轮换**：支持 `app_secret` 与 `app_secret_prev` 双活，轮换窗口 7 天。

**示例（PHP 侧）**

```php
$body = json_encode($payload, JSON_UNESCAPED_UNICODE|JSON_UNESCAPED_SLASHES);
$ts = time(); $nonce = bin2hex(random_bytes(16));
$raw = implode("\n", ['POST', '/internal/v1/keys', '', hash('sha256', $body), (string)$ts, $nonce]);
$sign = hash_hmac('sha256', $raw, $secret);
```

---

## 9. V10 模块（商品化）

目录 `public/plugins/server/llm_gateway/`，主类 `LlmGatewayModule.php`，必须实现 `metaData()`，其余按需：

| 方法 | 触发时机 | 我们要做的事 |
| --- | --- | --- |
| `metaData()` | 系统读取模块信息 | 返回 `display_name`、`version` |
| `testConnect($param)` | 后台"接口管理"状态灯 | 调 `/internal/v1/health`，返回 `status/msg` |
| `afterCreateFirstServer()` | 首次创建产品 | 建模块表（如需套餐档位与商品配置的对照表） |
| `createAccount($param)` | 开通/自动开通 | 用 `host`+`client`+`product`+`server` 组装 → 网关建订阅 + 发额度 + 生成首个 Key（可选） |
| `suspendAccount($param)` | 到期/超用/欠费 | 网关暂停订阅（Key 全置 suspended），注意 `suspend_type` 区分原因 |
| `unsuspendAccount($param)` | 恢复 | 恢复订阅与 Key |
| `terminateAccount($param)` | 删除产品 | 吊销订阅内全部 Key + 归档 |
| `renew($param)` | 续费成功 | 延长周期 + 重置周期额度（幂等键 `host_id + period_end`） |
| `get_host_flow`（hook） | 按量计费取数 | 返回期段内该 host 的消费金额/用量（若开通"按量补充包"） |

**商品配置 → 套餐的映射**（在 V10 商品配置里建自定义字段）：

| V10 商品配置项 | 网关 plan 字段 |
| --- | --- |
| `plan_code`（下拉：basic/pro/max） | `cz_plan.plan_code` |
| `quota_tokens`（数字，可选覆盖） | `quota_total` |
| `billing_mode`（订阅/余额/混合） | `billing_mode`（subscription/balance/hybrid） |
| `model_group`（标准/高级/全量） | `model_group` |
| `rpm/tpm/并发` | 限流参数 |

**注意**：`createAccount` 返回 `status=400` 时 V10 会让订单停在"开通失败"，务必返回 `msg` 便于客服排查；不要为了"看起来成功"而返回 200。

---

## 10. 支付与卡密（全部在 V10 侧）

> 依据 Q1 答复：**收款、开票、退款都在 V10 内部处理**。网关不实现任何支付逻辑（v0.1 的 `/pay/epay/*`、`cz_pay_order`、`cz_redeem_code` 全部取消）。

### 10.1 交付物：V10 支付网关插件 `czapi_epay`

位置 `public/plugins/gateway/czapi_epay/`（魔方"支付接口开发"约定的目录），入口类实现 `install()` / `uninstall()` / `Handle()`：

| 项 | 说明 |
| --- | --- |
| 形态 | V10 后台「支付接口」里新增一个"易支付"网关，配置 `pid / key / api_url`，启用后与支付宝、微信等标准网关并列 |
| 发起支付 | `Handle()` 内按 V10 传入的订单号、金额、支付方式拼装易支付参数并跳转/返回二维码 |
| 提交 | `POST {epay}/submit.php`（跳转收银台）或 `/mapi.php`（返回二维码/链接，适合弹窗） |
| 必填参数 | `pid, type(alipay/wxpay/qqpay), out_trade_no, notify_url, return_url, name, money, sitename, sign, sign_type=MD5` |
| 签名 | 参数键名 ASCII 升序拼 `k=v&...`（剔除空值、`sign`、`sign_type`），末尾直接拼 `key`，MD5 小写 |
| 回调 | `GET notify_url?pid&trade_no&out_trade_no&type&money&trade_status=TRADE_SUCCESS&sign&sign_type` → 校验签名 → **交回 V10 的支付回调处理流程**入账 |
| 响应 | 输出纯文本 `success`（不带引号/JSON） |
| 兜底 | 每 5 分钟主动查单（`api.php?act=order&pid=&key=&out_trade_no=`），补偿丢回调 |
| 退款 | 易支付多为手动退款，V10 后台走原有退款流程即可 |

> 好处：充值、套餐、卡密共用同一套 V10 订单与支付体系，**开票/退款/对账口径天然统一**，网关侧零支付代码。

### 10.2 卡密（插件内实现）

- 生成：插件后台「卡密批次」生成 `batch_no` + N 张，每张 20 位 base32（去易混淆字符），库中只存 `sha256` + 前缀，导出 CSV 给渠道。
- 兑换：前台 `console/v1/llm/redeem`（`CheckHome`），限登录用户，每人每日 5 次失败上限（防爆破）；
  成功流程：`UPDATE ... SET redeemed_by/redeemed_at WHERE redeemed_at IS NULL`（事务内，防并发重复兑换）→ 写 `_txn` 单据 → 调网关 `/internal/v1/credit`（`ref_type=redeem_code, ref_id=卡密ID`）→ 失败则回滚并提示"处理中，稍后自动重试"。
- 有效期与批次禁用；支持"套餐型卡密"直接开通订阅（V10 侧生成一笔已支付订单 + 调模块 `createAccount`）。

### 10.3 V10 入账 → 网关加额度的可靠投递

```
V10 侧（插件 model/LlmApi.php）
  入账成功（充值/卡密/调账/退款）
        │
        ├─ 先写 _pushlog（status=pending，payload 含 ref_type/ref_id/amount）  ← 本地先落库，保证不丢
        ├─ 立即尝试 POST /internal/v1/credit
        │     ├─ 2xx          → pushlog status=done
        │     ├─ 4xx（幂等冲突）→ 视为已处理，status=done
        │     └─ 5xx/超时      → status=failed, retry_count++, next_retry_at=+1min（指数退避）
        └─ 定时任务（每 5 分钟 / day_cron）扫描 failed → 重投，最多 N 次后告警给管理员
```

- **幂等双重保障**：V10 侧 `_txn` 表唯一键 + 网关 `/internal/v1/credit` 的 `(ref_type, ref_id)` 唯一索引。
- 管理员在插件后台可查看 pushlog 队列并手动重投（客户投诉"充了没到账"时的自助修复入口）。

---

## 11. Vue3 前端范围

### 11.1 用户中心（`/console`，MVP 用 iframe 嵌进 V10）

- 概览：今日/本月 token、费用、剩余额度（套餐+余额）、到期时间、近 30 天趋势（ECharts）
- Key 管理：列表（名称/前缀/状态/最近使用/用量）、创建（一次性展示）、禁用、删除、IP 白名单、模型范围
- 模型列表：名称、上下文长度、能力标签（vision/tools/json）、倍率/单价、是否可用
- 用量明细：按日期/模型筛选、分页、导出 CSV（不含 prompt 内容）
- 充值入口：**跳转 V10 充值页**（不在网关收钱）；卡密兑换在 V10 插件页完成
- 文档：快速开始（curl / OpenAI SDK / LangChain 示例）

### 11.2 管理后台（`/admin`）

- 仪表盘：QPS、TTFT、渠道错误率、收入、Top 用户/模型、对账差异
- 渠道：CRUD、密钥池、连通性测试、模型拉取（`GET {base}/v1/models` 自动同步）、熔断状态
- 模型与定价：分组、倍率、上下文长度
- 用户与 Key：检索、封禁、调账（`adjust` 流水）
- 订阅：按 host 查询、手工续期/暂停、异常工单
- 财务：加额凭证（`credit_ledger`）流水、与 V10 单据对账报表、异常/缺口告警（**不含收款，收款在 V10**）
- 系统：限流默认值、日志保留、审计日志、App 密钥轮换

---

## 12. 安全设计

| 风险 | 措施 |
| --- | --- |
| Key 泄露 | 只存哈希；日志/前端永不回显明文；支持一键吊销；异常 IP 自动告警 |
| 内部接口被刷 | HMAC + 时间窗 + nonce + IP 白名单（可配 V10 出口 IP）+ 限流（每 app_id 100 QPS） |
| 重放 | nonce 300s 去重；关键写接口再叠加业务幂等键（host_id、out_trade_no） |
| 越权 | 所有前台查询强制 `client_id = get_client_id()` 过滤；后台操作写审计日志（操作人/对象/前后值） |
| 无凭证加额 | `/internal/v1/credit` 强制 `ref_type+ref_id` 且 `(ref_type,ref_id)` 唯一；无凭证直接 400 |
| 额度重复发放（V10 重试） | V10 `_txn` 唯一键 + 网关 `credit_ledger` 唯一键 + pushlog 重投可见 |
| 卡密爆破 | 每人每日 5 次失败上限 + 兑换接口按 client 限流 + 异常告警 |
| 提示词/数据合规 | 默认不存正文；如需留存，AES-GCM 加密 + 7 天自动清理 + 仅管理员可解 |
| 上游密钥泄露 | 渠道 key 加密存储（KMS 或 env master key + AES-256-GCM），页面只显示尾 4 位 |
| DDoS / 刷量 | 网关层 IP 限速、Key 级 RPM、异常模型调用突增告警 |

---

## 13. 测试策略（有限测试版 ★）

> 决策：**有限测试后直接上生产**。因此测试只保留"不做就一定出事"的项，其余用**生产侧保险**（§15.2）替代。原则：**能用开关关掉的就不测到完美，不能关掉的必须测到位。**

### 13.1 必做（不可省，合计约 4 人日）

| 优先级 | 项目 | 验收 |
| --- | --- | --- |
| P0 | **计费正确性**：倍率公式、预扣/结算、cached 折扣、边界（0 token、超长、中断） | 手工用例表 30 条全过；与 new-api 同模型同请求的费用偏差 < 1% |
| P0 | **额度幂等**：同一 `(ref_type,ref_id)` 重复投递 10 次 | 只加一次额度，余额不变 |
| P0 | **Key 鉴权链**：无效/过期/禁用/超额度/IP 不符 | 分别返回附录 B 的对应错误码 |
| P0 | **流式**：正常、上游中断、客户端提前断开 | 无 panic、无 goroutine 泄漏、额度按已输出结算 |
| P0 | **V10 全链路**：下单→开通→建 Key→调用→用量→到期暂停→续费→删除 | 一条链路跑通且数据一致 |
| P1 | 对账脚本：usage 聚合 = 订阅消耗 = 余额扣减 | 三条链路相等 |
| P1 | 上游故障切换：主渠道 5xx/超时 | 自动换渠道，客户端无感（非流式） |

### 13.2 降级为"生产观察"的项（不做前置测试）

| 项目 | 替代手段 |
| --- | --- |
| 500 并发压测 | 上线后用真流量观察；默认限流值设保守（RPM 60 / 并发 5），观察 3 天再调 |
| 长时间稳定性（soak） | Prometheus 内存/goroutine 曲线 + 每日重启窗口兜底 |
| 全 SDK 兼容矩阵 | 只测 openai-python + NextChat；其余靠"标准 OpenAI 协议"赌兼容（失败可快速修） |
| 主题全量适配 | 只在实际使用的 1–2 套主题上验证，其余照抄壳页面 |
| 灾备演练 | 用"一键回滚"替代（§15.2） |

### 13.3 单元测试范围（仍要做，成本低收益高）

倍率计算、签名待签串、易支付签名、额度 Lua 原子性、token 估算兜底。目标覆盖率 60%+ 关键路径 80%+。

---

## 14. 部署与运维

- **拓扑**：Nginx/Caddy（TLS 终止）→ Gateway ×2 → MySQL 主从 / Redis 哨兵。
- **镜像**：多阶段构建，`CGO_ENABLED=0`，镜像 < 30MB；前端产物 embed 进 Go（`//go:embed`）便于单文件部署。
- **配置**：`config.yaml` + env 覆盖；secret 走环境变量或 Docker secret。
- **迁移**：`goose up` 在启动时执行（或独立 init job）；**每次迁移必须可回滚**（直发生产的前提）。
- **备份**：MySQL 每日全量 + binlog；`usage_record` 分区归档；**上线前做一次恢复演练**（唯一不可省的数据安全项）。
- **升级**：滚动重启；内部 API 版本化（`/internal/v1`、`/internal/v2`），插件可滞后于网关。
- **Runbook**：渠道全挂、Redis 故障降级、V10 加额通知堆积、计费异常、一键回滚。

---

## 15. 里程碑与排期（约 7 周至生产，2 Go + 1 Vue3 + 1 PHP）

> 12 周 → 7 周：砍掉 P5/P6 的独立完善期，把"监控/告警/回滚"提前到与功能并行的 P0 阶段完成（因为它们是直发生产的前提，不能后置）。

| 阶段 | 周期 | 交付物 | 验收（硬门槛） |
| --- | --- | --- | --- |
| **P0 地基（含监控）** | W1 | monorepo、迁移框架（可回滚）、chi 路由、配置、CI、Docker Compose、**Prometheus 指标 + 3 条核心告警 + 一键回滚脚本**；同时做**最小穿透插件**（1.5–2 人日，V1–V5） | `make dev` 起；`/healthz` 通；迁移回滚成功；V1–V5 全过；**告警能真实触发一次** |
| **P1 转发核心** | W2–W3 | Key 鉴权、渠道/模型映射、SSE 直通、重试熔断、usage 落库、`/internal` DTO 冻结、**功能开关框架** | openai-python 跑通流式/非流式；断流不泄漏；开关能灰度关闭整条链路 |
| **P2 计费与额度** | W4 | 倍率引擎、预扣/结算、订阅、余额账本、`/internal/v1/credit`、加额投递重试、异步 Worker | §13.1 的 P0 计费 + 幂等用例全过；对账脚本 3 条链路相等 |
| **P3 插件 MVP** | W5 | 插件全量骨架、签名客户端、前台 Key/模型/兑换 API、iframe 壳（实际主题）、后台仪表盘、语言包 | 测试站内建/查/禁 Key 成功；用量与网关一致 |
| **P4 模块 + 支付 + 后台** | W6 | 模块 8 方法、商品映射、`order_paid`→开通→加额、到期暂停、续费重置、`czapi_epay` 网关插件、卡密、后台（渠道/封禁/调账/pushlog） | 下单→发 Key 全链路；易支付真实小额支付成功且回调幂等；到期 402；卡密只加一次 |
| **P5 灰度上生产** | W7 前 3 天 | 内部账号 + 3–5 个真实客户灰度；影子双跑（见 §15.2） | 连续 72h 无 P1 级故障；对账无差异 |
| **P6 全量** | W7 后 2 天 | 开放购买，保守限流 | 观察 7 天，曲线平稳后逐步放宽限流 |

**关键路径与并行**
- **P0 的监控/回滚必须和功能同时做**，不是最后补——直发生产唯一的依靠就是它。
- P3（PHP）与 P2（Go）第 4 周起并行；都依赖 P1 冻结的 `/internal` DTO。
- 砍掉的东西：**应用商店上架、原生插件页（方式 B）、Claude/Gemini 原生协议、`get_host_flow` 按量出账**，全部推到生产稳定后（P7+）。

### 15.2 生产直发的五道保险（★ 这是压缩测试的交换条件）

| # | 保险 | 具体做法 | 出问题时的效果 |
| --- | --- | --- | --- |
| 1 | **功能开关（Feature Flag）** | 全局开关 + 按客户开关：`gateway_enabled`、`billing_enforced`、`module_autocreate_enabled`、`recharge_enabled`。配置热加载，无需重启 | 任意模块可单独关闭，业务不停 |
| 2 | **额度封顶（防爆量）** | 每个 Key 每日消费上限（默认 50 元）；每个账号每月上限；全站日总消耗熔断线。超限返回 402 并告警 | 计费 bug 或被盗刷时，损失有天花板 |
| 3 | **影子双跑（Shadow）** | 灰度期新老并行：请求照常走上游，用量同时写"新计费引擎"和"影子表（只记不扣）"，每日比对差异 | 计费逻辑错误在上线前就能看见，不伤钱 |
| 4 | **一键回滚** | 网关：`docker compose` 保留上一版本镜像 + DB 迁移 `down`；V10 侧：插件/模块/网关插件各自可停用（V10 后台可关闭） | 5 分钟内回到上一状态 |
| 5 | **值班与告警** | 上线首 7 天：渠道错误率 > 5%、计费延迟 > 60s、余额负值、加额失败堆积、`/v1` 5xx > 1% 全部推送到手机 | 故障 10 分钟内有人知道 |

### 15.3 上线前必须人工确认的 6 条清单

- [ ] 数据库迁移 `up` → `down` → `up` 全通过（在预发库上）
- [ ] 备份已做且**实际恢复成功过一次**
- [ ] 计费用例表 30 条 + 幂等用例全部通过
- [ ] 灰度客户名单已确认，额度上限已配置
- [ ] 回滚脚本演练过一次（不是"写了但没跑过"）
- [ ] 值班人 + 告警群已就位，Runbook 打印在手边

---

## 16. 风险与对策

| 风险 | 影响 | 对策 |
| --- | --- | --- |
| V10 文档与真实源码不一致（基类命名空间、中间件、模板机制） | 插件返工 | **已用官方 idcsmart_ticket 源码核对并写入 docs/v10-notes.md**；P0 穿透测试再在目标实例上跑 V1–V5 验证 |
| 前台主题不在已知列表（5 套） | 页面打不开 | P0 穿透测试一次性确认站点实际主题；iframe 壳每套仅 1 个文件，新增主题成本 ≈ 10 分钟 |
| V10 升级导致插件失效 | 线上故障 | 插件只依赖稳定 hook + 版本化 `/internal/v1`；升级前跑回归清单 |
| 计费与实际不符（上游不返回 usage） | 客诉/亏钱 | 强制 `include_usage`；无 usage 时用 tokenizer + 标 `estimated`；对账报表暴露估算比例 |
| 流式中断导致重复扣费 | 客诉 | 预扣+结算模型；`request_id` 幂等；已输出即按预扣额结算并记录 `partial` |
| V10 加额通知丢失/重复 | 用户充了没到账 / 白拿额度 | 本地 pushlog 先落库再发 + 网关 `(ref_type,ref_id)` 唯一 + 后台可查可重投 |
| 易支付网关不稳定/风控 | 收款失败（影响所有 V10 商品） | 该插件仅作为**可选**支付方式，与支付宝/微信并列；支持多套易支付配置；主动查单兜底 |
| 上游封号/涨价 | 毛利为负 | 渠道优先级 + 倍率可热更新；监控单位成本 |
| 首期范围过大 | 延期 | 严格执行 MVP：P0（含穿透）→P1–P3 是最小可交付闭环，P4 的 `get_host_flow` 按量可延后 |

### 16.1 「有限测试直发生产」专项风险 ★

| 风险 | 后果 | 唯一可行的对冲 |
| --- | --- | --- |
| **计费逻辑错误上线**（倍率算错、重复扣、漏扣） | 直接亏钱或客诉退款 | **影子双跑 3 天**（只记不扣）+ 每日差异比对；`billing_enforced` 开关可一键降级为"只记录不扣费" |
| **额度发放错误**（V10 通知重复/丢失） | 白送额度或用户充值不到账 | `(ref_type,ref_id)` 唯一 + pushlog 重投幂等；上线前重复投递 10 次必须只加一次（P0 用例） |
| **流量突增打垮网关** | 全站不可用 | 保守限流开路（RPM 60/并发 5）+ 每 Key 日上限 + 全站日熔断线；观察 7 天再放宽 |
| **Key 被恶意刷量/盗用** | 账单爆炸 | 每 Key 日/月消费封顶 + 异常用量突增告警（同比 5 倍）+ 一键封 Key |
| **V10 插件在生产报错** | 用户无法建 Key（影响售卖） | 插件/模块/支付插件**均可单独停用**；停用后回落到"人工开 Key"的客服 SOP |
| **迁移失败导致数据损坏** | 灾难 | 迁移 `down` 必须验证过 + 上线前真实恢复演练一次（§15.3 清单） |
| **发现致命 bug 但没回滚路径** | 长时间停机 | 保留上一版本镜像 + 回滚脚本演练过一次（不是"写了没跑"） |
| 缺失长时间稳定性数据 | 偶发内存泄漏/OOM | Prometheus 看 goroutine 与 RSS 曲线；每日凌晨滚动重启作为兜底 |

> 结论：**测试可以压缩，但"封顶 + 开关 + 影子 + 回滚 + 告警"这五件不能压缩**。它们不是测试的补充，是直发生产的准入条件。

---

## 17. 待确认问题

### 17.1 已答复（v0.2）

| # | 问题 | 答复 | 落地章节 |
| --- | --- | --- | --- |
| Q1 | 充值款谁开票/谁退款 | **直接在 V10 内部处理** | §0、§2、§5.4、§10 |
| Q2 | 目标版本与测试环境 | **先做最小限度测试**（随后直发生产） | §13、§15、docs/v10-notes.md §9 |
| Q3 | 额度维度/计价 | **参考 new-api** | §6.6 |
| Q4 | 上游模型范围 | **参考 new-api**（OpenAI 兼容全量） | §6.5、§17.2 Q4' |
| Q2 补充 | 测试到什么程度 | **有限测试后直接上生产**（约 7 周至生产） | §13、§15、§15.2、§16.1 |

### 17.2 仍需确认（不阻塞 P0/P1，但会影响 P2 之后）

| # | 问题 | 为什么需要 |
| --- | --- | --- |
| Q2' | 目标 V10 的**确切版本号**、后台目录名（`DIR_ADMIN` 实际值）、站点实际使用的**前台主题目录名**、能否给一个可装插件的测试站 | P0 的必要输入；没有实例就只能靠源码静态推断 |
| Q4' | 首批上线要开**哪几个模型**（new-api 的模型面很宽，但首批建议收敛到 5–10 个），是否已有上游密钥与额度 | 决定渠道表初始化数据与定价表 |
| Q5 | 是否需要**纯内网/私有化部署**（上游是内网 vLLM，用户在内网） | 影响 TLS、回调地址、部署形态 |
| Q6 | 付费用户数与峰值 RPM / 平均 token 预估 | 决定实例规格与限流默认值 |
| Q7 | 是否需要**多站点/多品牌**（一套网关服务多个 V10 站点，各自独立定价） | 决定 `v10_site_id` 隔离粒度 |
| Q8 | 是否需要给客户**导出对账单 / SLA 报表** | 后台报表范围 |
| Q9 | 插件/模块是否要**上架魔方应用商店** | 决定是否必须做方式 B 的原生插件页与安装包规范 |
| Q10 | 团队人员构成（Go / Vue3 / PHP 各几人，是否熟悉 ThinkPHP 与 V10 二次开发） | 排期与分工 |

---

## 附录 A：V10 文档索引（开发必读）

| 主题 | 链接 |
| --- | --- |
| 开发文档总览（适用 V10.5.6） | https://docs.idcsmart.com/docs/V10业务系统开发文档 |
| 插件开发（目录结构、控制器、路由、多语言、后台导航） | https://docs.idcsmart.com/docs/V10业务系统开发文档/插件开发 |
| 模块开发（metaData/createAccount/suspend/unsuspend/terminate/renew/testConnect） | https://docs.idcsmart.com/docs/V10业务系统开发文档/模块开发 |
| Hook 开发（共 165 个） | https://docs.idcsmart.com/docs/V10业务系统开发文档/Hook开发 |
| 定时任务 Hook（`get_host_flow` 按量取数、各类 cron） | https://docs.idcsmart.com/docs/V10业务系统开发文档/Hook开发/系统与自动任务Hook/定时任务 |
| V10 开发规范 | https://docs.idcsmart.com/docs/V10业务系统开发文档/V10开发规范 |
| 前台接口（JWT、`{{baseUrl}}`、`console/v1`） | https://docs.idcsmart.com/docs/V10业务系统API/前台接口 |
| 后台接口（`{{DIR_ADMIN}}`） | https://docs.idcsmart.com/docs/V10业务系统API/后台接口 |
| PHP 函数与方法（941 条，含 `idcsmart_addon_url`、`lang_plugins` 等） | https://docs.idcsmart.com/docs/V10业务系统API/函数 |
| 官方支付接口开发（可参考其回调与签名风格） | https://docs.idcsmart.com/docs/V10业务系统开发文档/支付接口开发 |

### 已核实的 V10 关键事实（文档结论 + 源码实测，以 ★ 为准）

- 插件路径：`public/plugins/addon/{插件名}`；主类 = 目录名大驼峰 `.php`，`extends \app\common\lib\Plugin`；`$info` 含 `name/title/description/author/version`；`install()/uninstall()` 里直接跑 SQL。★
- 表名前缀：`idcsmart_addon_{插件名}_*`。★
- 插件页面地址：后台 `{站点}/{DIR_ADMIN}/plugin/{插件名}/index.html`（页面间跳转用 `.htm`）；也可用 `addon?_plugin=&_controller=&_action=` 形式。★
- **控制器基类（★修正）**：前台 `app\event\controller\PluginBaseController`、后台 `app\event\controller\PluginAdminBaseController`；客户/管理员 ID 用全局函数 `get_client_id()` / `get_admin_id()`。（文档写的 `app\admin\controller\...` 与源码不符）
- **中间件（★补充）**：`ParamFilter`（通用）、**`Check`（免登录）**、`CheckHome`（前台登录）、`CheckAdmin`（后台登录）、`RejectRepeatRequest`（防重复提交）。
- ★ **模板机制（最重要）**：插件页面是 **HTML 片段 + 独立 JS 创建 Vue 实例**（不是 PHP 模板引擎）。前台全局为 **Vue2 + Element UI（`el-*`）**，后台为 **Vue2 + TDesign（`t-*`）**；每页 = `xxx.html` + `js/xxx.js` + `api/xxx.js` + `css/`。旧版 `.php` 模板已废弃（放在 `bak/`）。
- ★ **前台模板按主题分目录**：`template/clientarea/pc/{default,mf801,mf_pc_701}/`、`mobile/{mfm201,mfm301}/`，每套主题各自一份页面。
- ★ **语言**：PHP 用 `lang/zh-cn.php` + `lang_plugins('key')`；前端用 `template/{admin,clientarea}/lang/index.js` 挂 `window.lang`（系统）+ `window.plugin_lang`（插件），在 JS 里 `Vue.prototype.lang = Object.assign(window.lang, window.plugin_lang)`。
- ★ **导航**：`sidebar_clientarea.php` 是**前台**导航（源码启用），`sidebar.php` 是后台导航；条目 `{name(语言键), url, icon(TDesign 图标名), in, child[]}`。
- ★ **权限**：`auth.php`（后台）/`auth_clientarea.php`（前台）定义权限点与 `auth_rule`，前端用 `v-permission`。
- ★ **插件配置**：`config.php` 返回 `{键: {title,type(text/password/textarea/checkbox/radio/select),value,tip}}`，后台自动渲染，无需自写配置页。
- ★ **审计日志**：`active_log($描述, $标识, $对象ID)`；返回体约定 `json(['status'=>200,'msg'=>...,'data'=>...])`。
- 模块路径 `public/plugins/server/{模块名}`，主类同名；`hooks.php` 在模块加载即引入，需自行判断适用范围；默认表名建议 `idcsmart_module_{模块}_{具体}`。
- 前台/后台接口鉴权为 Bearer JWT（`{{CLIENT_TOKEN}}` / `{{ADMIN_TOKEN}}`）；`{{DIR_ADMIN}}` 为后台目录变量。
- 按量取数钩子 `get_host_flow` 参数：`host`(HostModel)、`module`、`start_time`、`end_time`；暂停钩子带 `suspend_type`（overdue/overtraffic/certification_not_complete/other/downstream）与 `suspend_reason`。
- 订单相关钩子示例：`before_order_create`、`after_order_create`、`order_paid`；产品相关：`after_host_delete`、`before_host_renewal_first`、`get_auto_renew_host_id`、`host_list_where_query_append`。

> 带 ★ 的条目已在官方 `idcsmart_ticket` 4.2.2 源码中核实，详见 [docs/v10-notes.md](docs/v10-notes.md)。**Hook 名仍需在目标实例上核对**（文档标注适用 10.5.6，实测插件为 4.2.2）。

---

## 附录 B：错误码约定（OpenAI 风格）

| HTTP | code | 场景 | 建议文案 |
| --- | --- | --- | --- |
| 401 | `invalid_api_key` | Key 不存在/已删除 | Invalid API key provided. |
| 403 | `key_suspended` | Key 被禁用/订阅暂停 | API key is suspended. Please renew your plan. |
| 403 | `model_not_allowed` | 套餐不含该模型 | Model not available in your plan. |
| 403 | `ip_not_allowed` | IP 白名单 | Request from a non-whitelisted IP. |
| 402 | `insufficient_quota` | 额度/余额耗尽 | Quota exceeded. Please top up. |
| 429 | `rate_limit_exceeded` | RPM/TPM/并发超限 | Rate limit reached, retry after {n}s. |
| 400 | `invalid_request_error` | 参数错误 | 透传上游原始错误 |
| 502 | `upstream_error` | 上游 5xx / 全渠道失败 | Upstream service error, please retry. |
| 504 | `upstream_timeout` | 超时 | Upstream timeout. |
| 503 | `channel_unavailable` | 无可用渠道 | Service temporarily unavailable. |

响应头附带 `X-CZ-Request-Id`，便于工单排查。

---

## 附录 C：与 new-api / one-api 的差异取舍

| 能力 | new-api | 本方案 | 说明 |
| --- | --- | --- | --- |
| 用户体系 | 自建注册/登录 | **复用 V10** | 用户只在 V10 存在，网关侧重"账户映射"而非"账号体系" |
| 商品/订单/收款 | 内置套餐、充值、易支付 | **全部在 V10**（商品=套餐，充值=余额，易支付=V10 支付网关插件） | 网关只做额度账本，账务口径单一 |
| 后台 | 独立管理台 | 独立 Vue3 后台 + V10 后台插件页 | V10 后台只做"看与封禁"，重配置在网关后台 |
| 计费 | ratio 倍率体系 | 同左（model_ratio / completion_ratio / group_ratio / cached） | 直接对齐用户熟悉的计费直觉 |
| 渠道 | 多 provider | 先只做 OpenAI 兼容 | 减少首期面积，协议差异靠 provider adapter 预留 |
| 部署 | 单体 | 网关与 V10 解耦 | 流量隔离，独立扩缩容 |
| 许可证 | —（待核实） | 自研 | 只借鉴思路与接口形状，不复制源码（见 §6.6 末注） |

---

## 附录 D：官方插件源码 vs 官方文档（差异清单）

实测对象：`idcsmart_ticket` 4.2.2（官方工单插件）。**凡冲突，以本表为准。**

| # | 项目 | 官方文档说法 | 源码实测 | 我们要怎么做 |
| --- | --- | --- | --- | --- |
| 1 | 前台控制器基类命名空间 | `app\admin\controller\PluginBaseController` | **`app\event\controller\PluginBaseController`** | 用 `app\event\...` |
| 2 | 免登录中间件 | 未明确 | **`\app\http\middleware\Check`** | 回调路由用 `Check` + 自校验签名 |
| 3 | 防重复提交 | 未提及 | **`\app\http\middleware\RejectRepeatRequest`** | 创建/兑换类接口挂上 |
| 4 | 前台模板机制 | 描述为 `.php` 模板 | **`.html` 片段 + 独立 `js/*.js` 创建 Vue2 实例**（PHP 模板已是 `bak/` 遗留） | 按 HTML+Vue2 规范写壳页面 |
| 5 | 前台主题适配 | 未强调 | **必须按主题目录各放一份**（pc/default、pc/mf801、pc/mf_pc_701、mobile/mfm201、mobile/mfm301） | iframe 壳 × 5（+ 站点实际主题） |
| 6 | hook 写法 | `hooks.php` + `add_hook()` | 官方插件**无 hooks.php**，用主类小驼峰方法（`afterAdminDelete`） | 统一用 `hooks.php`（事件多，便于集中管理），保留主类方法兼容 |
| 7 | 安装 SQL | 提到 `install.sql` | **直接写在 `install()` 里执行** | 写在 `install()` 中，便于事务与版本判断 |
| 8 | 表名规范 | `idcsmart_module_*`（模块） | 插件用 **`idcsmart_addon_{插件名}_*`** | 按 `idcsmart_addon_idcsmart_llm_gateway_*` |
| 9 | 文件编码 | 未说明 | **UTF-8 无 BOM**（字节级验证） | 全项目 UTF-8 |
| 10 | 前端语言注入 | `window.module_lang` | **`window.plugin_lang` + `window.lang`**（`Vue.prototype.lang = Object.assign(...)`） | 用 `plugin_lang` |
| 11 | 配置页 | 需自行实现 | **`config.php` 定义表单，后台自动渲染** | 用 `config.php`，不写配置页 |
| 12 | 权限点 | 有提及 | `auth.php` / `auth_clientarea.php` + `v-permission` | 为后台操作定义权限点 |

---

## 附录 E：下一步（等你确认后我继续）

按"有限测试直发生产"，W1 就要并行开工，因此**开工前只卡一件事**：

1. **P0 必需输入**（没有就无法做穿透插件）：测试站/生产站地址、V10 版本、`DIR_ADMIN` 实际值、前台主题目录名；以及是否允许我在生产站上先装一个只读探针插件（不影响业务）。
2. 我产出 **最小穿透插件代码包**（含 V1–V5 验证清单与回填模板）。
3. 同时产出 **《/internal/v1 接口契约 v1》**（OpenAPI 3.0 + 签名示例 + 错误码），供 Go 与 PHP 两端并行。
4. **P0 一起交付**（不后置）：Prometheus 指标 + 3 条核心告警 + 一键回滚脚本 + 功能开关框架。

**关于"直接上生产"的两点提醒（需要你确认接受）**
- **影子双跑 3 天不能砍**。它是"有限测试"唯一能替代长周期测试的等价物：只记录不扣费，靠每日差异比对暴露计费错误。砍了它，等于计费 bug 直接打在真实账单上。
- **每 Key 日消费上限（默认 50 元）建议默认开启**。它把"计费 bug 或 Key 被盗"从"损失无上限"变成"损失有天花板"，是直发生产最便宜的保险。

先要"能跑的最小闭环"的话，建议顺序：`/v1/chat/completions` 转发 → Key 鉴权 → 额度预扣 → 用量落库 → 插件建 Key。

---

*本计划为规划稿 v0.3。已吸收 Q1–Q4 答复、"有限测试直发生产"策略与官方插件源码实测结论；Q5–Q10 答复后更新 §2 / §15 / §17。*
