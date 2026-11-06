# CZAPI 控制台 API 契约 v1（`/console/v1` + `/admin/v1`）

> 冻结文件。前端（`web/`）与 HTTP 层（`internal/api/`）都以此为准。
> 与 `docs/internal-api-v1.md` 的区别：那是 **V10 插件 → 网关（服务端到服务端、HMAC）**；
> 本文件是 **浏览器 → 网关（JWT / SSO cookie）**。两者不要混用。

- 前缀：`/console/v1`（用户中心）、`/admin/v1`（管理后台）
- 认证：`Authorization: Bearer <jwt>`（管理后台）；用户中心用 SSO 下发的一次性票据换 HttpOnly `cz_ss` cookie
- 响应信封：**与内部接口一致** `{"code":0,"msg":"ok","data":{...}}`
- 分页：请求 `page`（从 1）、`page_size`（默认 20、≤200）；响应 `{"total":n,"page":p,"page_size":s,"items":[...]}`
- 时间：**一律 unix 秒（int64）**，前端自行格式化
- 金额字段命名以 `_micros` 结尾，单位微元（1 元 = 1_000_000），前端除 1e6 显示
- 错误码沿用 `errs.Code`：400/401/402/403/404/409/429/500/503

---

## 1. 用户中心 `/console/v1`

### 1.1 SSO 入驻

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/console/v10-sso?ticket=xxx` | V10 插件跳转入口。校验一次性票据 → 下发 HttpOnly cookie `cz_ss` → 302 到 `/console/` |

票据由插件调 `GET /internal/v1/console/sso-ticket` 换取（见 §1.2 末尾）。

### 1.2 概览 `GET /console/v1/overview`

```json
{
  "account": { "v10_client_id": 1234, "email": "a@b.com", "status": 1 },
  "balance_micros": 50000000,
  "today":  { "requests": 20, "prompt_tokens": 15000, "completion_tokens": 5000, "cost_micros": 750000 },
  "month":  { "requests": 320, "prompt_tokens": 240000, "completion_tokens": 80000, "cost_micros": 12000000 },
  "subscriptions": [
    { "subscription_id": 9, "v10_host_id": 555, "plan_code": "pro", "plan_name": "专业版",
      "status": "active", "billing_mode": "hybrid",
      "period_start": 1761945600, "period_end": 1764547200,
      "quota_total": 10000000, "quota_used": 2500000, "quota_remaining": 7500000 }
  ],
  "trend": [ { "day": "2026-11-05", "requests": 20, "prompt_tokens": 15000, "completion_tokens": 5000, "cost_micros": 750000 } ],
  "recharge_url": "https://v10.example.com/plugin/idcsmart_llm_gateway/recharge.html"
}
```

> `recharge_url` 由后端配置下发，前端只做跳转（**网关不收款**，plan §0）。

### 1.3 Key 管理

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/console/v1/keys` | 列表（含 30 天用量） |
| POST | `/console/v1/keys` | 创建，**明文只返回一次** |
| PATCH | `/console/v1/keys/{id}` | 改名 / 改模型范围 / 改 IP 白名单 / 启停 |
| DELETE | `/console/v1/keys/{id}` | 吊销 |

`POST` 请求体：

```json
{ "name": "生产环境", "subscription_id": 9, "models": ["deepseek-chat"], "ip_whitelist": ["1.2.3.0/24"], "expires_at": 1793836800 }
```

`POST` 响应 `data`：

```json
{ "key_id": 321, "key": "sk-xxxx", "key_prefix": "sk-xxxx", "name": "生产环境", "status": "active", "created_at": 1762300000 }
```

列表项：

```json
{ "key_id": 321, "key_prefix": "sk-abcdEFGH", "name": "生产环境", "status": "active",
  "subscription_id": 9, "v10_host_id": 555, "models": ["deepseek-chat"], "ip_whitelist": [],
  "expires_at": null, "last_used_at": 1762299000, "created_at": 1762200000,
  "usage_micros_30d": 1234567, "requests_30d": 42 }
```

`PATCH` 请求体（只带要改的字段）：`name`、`status`(`active`\|`suspended`)、`models`、`ip_whitelist`、`expires_at`。

### 1.4 模型 `GET /console/v1/models`

返回**该账户套餐可用**的模型。

```json
{ "total": 1, "items": [ { "model_name": "deepseek-chat", "display_name": "DeepSeek Chat",
  "group_name": "default", "context_len": 65536, "capabilities": ["tools","json"],
  "ratio": { "model_ratio": 0.5, "completion_ratio": 2.0, "cached_ratio": 0.5 },
  "price_hint": { "input_micros_per_1m": 500000, "output_micros_per_1m": 1000000 } } ] }
```

### 1.5 用量

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/console/v1/usage/summary` | 参数 `from`,`to`,`group_by`(day\|model\|key\|host) |
| GET | `/console/v1/usage/logs` | 参数 `from`,`to`,`api_key_id`,`model`,`page`,`page_size`；**不含 prompt 正文** |
| GET | `/console/v1/usage/export` | 同 logs 参数，返回 CSV（`text/csv`） |

`summary` → 同 `docs/internal-api-v1.md` §2.10（`totals` + `buckets`）。
`logs` → 同 `docs/internal-api-v1.md` §2.11。

### 1.6 文档 `GET /console/v1/docs`

```json
{ "base_url": "https://api.example.com", "examples": { "curl": "...", "python": "...", "node": "...", "langchain": "..." } }
```

---

## 2. 管理后台 `/admin/v1`

### 2.1 登录

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | `/admin/v1/login` | `{username,password}` → `{token, expires_at, admin:{id,username,role}}` |
| POST | `/admin/v1/logout` | 无内容 |
| GET | `/admin/v1/me` | 当前管理员 |

### 2.2 仪表盘 `GET /admin/v1/dashboard`

```json
{
  "requests_today": 1200, "cost_today_micros": 45000000, "cost_month_micros": 900000000,
  "error_rate_5m": 0.012, "qps_5m": 3.4, "ttft_p95_ms_5m": 820,
  "active_keys": 42, "active_subscriptions": 18, "accounts": 25,
  "channels": [ { "channel_id": 1, "name": "deepseek", "status": 1, "open": false, "error_rate": 0.01 } ],
  "top_models": [ { "model": "deepseek-chat", "requests": 900, "cost_micros": 30000000 } ],
  "top_accounts": [ { "account_id": 77, "v10_client_id": 1234, "email": "a@b.com", "cost_micros": 20000000 } ],
  "trend": [ { "day": "2026-11-05", "requests": 1200, "cost_micros": 45000000 } ],
  "reconcile": { "last_run": 1762290000, "diff_micros": 0, "ok": true }
}
```

### 2.3 渠道 `/admin/v1/channels`

| 方法 | 路径 |
| --- | --- |
| GET | `/admin/v1/channels` |
| POST | `/admin/v1/channels` |
| GET | `/admin/v1/channels/{id}` |
| PATCH | `/admin/v1/channels/{id}` |
| DELETE | `/admin/v1/channels/{id}` |
| POST | `/admin/v1/channels/{id}/test` | 连通性探测 → `{ok,message,latency_ms}` |
| POST | `/admin/v1/channels/{id}/sync-models` | 拉取上游 `/v1/models` 并合并 |

渠道对象（**密钥只回显尾 4 位**）：

```json
{ "channel_id": 1, "name": "deepseek", "provider": "openai", "base_url": "https://api.deepseek.com",
  "models": ["deepseek-chat"], "model_map": {"gpt-4o-mini":"deepseek-chat"},
  "weight": 100, "priority": 0, "max_concurrency": 100, "timeout_ms": 300000, "status": 1,
  "key_pool": [ { "index": 0, "masked": "****abcd", "weight": 1, "status": "active" } ],
  "health": { "open": false, "error_rate": 0.0, "samples": 12 } }
```

写入时 `key_pool` 用 `{ "secret": "sk-...", "weight": 1, "status": "active" }`（**只写不回读明文**）。

### 2.4 模型 `/admin/v1/models`

| 方法 | 路径 |
| --- | --- |
| GET | `/admin/v1/models` |
| POST | `/admin/v1/models` | upsert by `model_name` |
| PATCH | `/admin/v1/models/{name}` |
| DELETE | `/admin/v1/models/{name}` |

对象：`model_name, display_name, group_name, context_len, model_ratio, completion_ratio, cached_ratio, input_micros, output_micros, capabilities, status`。

### 2.5 套餐 `/admin/v1/plans`

CRUD，对象同 `domain.Plan`（`plan_code, name, quota_tokens, period, rpm_limit, tpm_limit, concurrency, model_group, max_keys, price_cents, group_ratio, status`）。

### 2.6 账户与 Key

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/admin/v1/accounts` | 参数 `q`（邮箱/client_id 模糊）、`page`、`page_size` |
| GET | `/admin/v1/accounts/{id}` | 详情（含订阅、Key、近 30 天用量） |
| POST | `/admin/v1/accounts/{id}/adjust` | 调账 `{amount_micros, remark, ref_id}` → 走 `ref_type=admin_adjust` 幂等 |
| PATCH | `/admin/v1/accounts/{id}` | 改 `status`、`allow_negative` |
| GET | `/admin/v1/keys` | 跨账户检索 `q`、`status`、`account_id` |
| PATCH | `/admin/v1/keys/{id}` | 封禁/解封 |

### 2.7 订阅

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/admin/v1/subscriptions` | 参数 `status`、`account_id`、`v10_host_id` |
| GET | `/admin/v1/subscriptions/{id}` | 详情 |
| POST | `/admin/v1/subscriptions/{id}/suspend` | 手工暂停 |
| POST | `/admin/v1/subscriptions/{id}/resume` | 手工恢复 |
| POST | `/admin/v1/subscriptions/{id}/reset-quota` | 手工重置周期额度 |

### 2.8 财务 `GET /admin/v1/ledger`

参数 `account_id`、`type`、`ref_type`、`from`、`to`、`page`、`page_size`。
返回 `credit_ledger` 条目：`{id, account_id, type, amount_micros, balance_after, ref_type, ref_id, remark, created_at}`。

### 2.9 对账 `GET /admin/v1/reconcile`

参数 `day`（YYYY-MM-DD，默认昨天）。

```json
{ "day": "2026-11-04", "usage_cost_micros": 45000000, "subscription_consumed_micros": 45000000,
  "balance_consumed_micros": 0, "ok": true, "diff_micros": 0,
  "details": [ { "account_id": 77, "v10_host_id": 555, "usage_micros": 1000000, "ledger_micros": 1000000, "diff_micros": 0 } ] }
```

### 2.10 功能开关 `/admin/v1/flags`

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/admin/v1/flags` | 全部开关（全局 + 按账户覆盖） |
| PUT | `/admin/v1/flags/{key}` | `{value, scope}`（`scope` 空 = 全局） |

开关键：`gateway_enabled`、`billing_enforced`、`module_autocreate_enabled`、`recharge_enabled`（plan §15.2）。

### 2.11 系统

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/admin/v1/settings` | 只读展示当前生效配置（脱敏） |
| GET | `/admin/v1/audit` | 审计日志分页 |
| GET | `/admin/v1/sites` | V10 站点列表 |
| POST | `/admin/v1/sites` | 注册站点，返回 `app_id` + **仅显示一次的 secret** |
| POST | `/admin/v1/sites/{app_id}/rotate-secret` | 轮换（旧 secret 保留 7 天为 prev） |

---

## 3. 补充的内部接口（V10 → 网关）

在 `docs/internal-api-v1.md` 之外新增一个（同期实现）：

### `POST /internal/v1/console/sso-ticket`

插件为已登录客户申请一次性入驻票据。

**请求**：`{ "v10_client_id": 1234, "email": "a@b.com" }`
**响应 `data`**：`{ "ticket": "64位hex", "expires_in": 60, "sso_url": "https://api.example.com/console/v10-sso?ticket=..." }`

票据一次性、60 秒过期、绑定 `v10_client_id`。
