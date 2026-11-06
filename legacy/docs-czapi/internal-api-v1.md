# CZAPI 内部契约 v1（冻结版）

> 本文件是 Go 网关与 V10 插件（PHP）并行开发的**唯一接口事实来源**。
> 与 `plan.md` 冲突时以本文件为准（本文件是 `plan.md` §6.3 的细化落地）。
> 变更规则：**只能追加**，不能修改已发布字段语义；破坏性变更须升 `v2` 前缀。

- 冻结日期：2026-11-05
- 对应实现：`internal/api/internal`（服务端）、`v10/idcsmart_llm_gateway/model/LlmApi.php`（客户端）
- 统一前缀：`/internal/v1`
- 传输：HTTPS + JSON（`Content-Type: application/json; charset=utf-8`）

---

## 1. 通用约定

### 1.1 响应信封

**所有** `/internal/v1` 响应都是统一信封：

```json
{ "code": 0, "msg": "ok", "data": { } }
```

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `code` | int | `0` 成功；非 0 为业务错误码（见 §1.4） |
| `msg` | string | 人类可读信息，可直接展示在 V10 页面 |
| `data` | object \| null | 业务数据；出错时为 `null` |

> 与 OpenAI 兼容层（`/v1/*`）的 `{"error":{...}}` 格式**不同**。两层各自统一，不混用。

### 1.2 请求头（签名）

```
X-CZ-App-Id:    {app_id}
X-CZ-Timestamp: {unix 秒}
X-CZ-Nonce:     {32 位十六进制随机串}
X-CZ-Signature: {hex 小写}
Content-Type:   application/json
```

待签串（`\n` 连接，均为 ASCII，**无尾随换行**）：

```
{METHOD_UPPER}
{PATH}                     # 含前缀，如 /internal/v1/keys
{SORTED_QUERY}             # key 升序；无查询串时为空行（仍保留该行的 \n）
{SHA256_HEX_OF_BODY}       # 请求体原始字节的 sha256 hex；空体为 sha256("")
{TIMESTAMP}
{NONCE}
```

签名 = `hex(HMAC_SHA256(app_secret, 待签串))`。

**校验规则（服务端）**：`app_id` 存在且启用 → `|now-ts| ≤ 300s` → nonce `SETNX` 成功 → 常数时间比较签名 → 失败统一 `HTTP 401` + `code=40100`。

**响应签名（可选校验）**：响应头 `X-CZ-Sign-Server: hex(HMAC(secret, sha256hex(body)+timestamp))`，
另有 `X-CZ-Timestamp-Server`。插件可开关是否校验。

### 1.3 幂等

| 接口 | 幂等键 | 重复调用的结果 |
| --- | --- | --- |
| `POST /credit` | `(ref_type, ref_id)` | 200，`data.applied=false`，余额不变 |
| `POST /subscriptions` | `v10_host_id` | 200，`data.created=false`，返回既有订阅 |
| `POST /subscriptions/{id}/renew` | `period_end`（目标周期） | 200，`data.applied=false` |
| `POST /keys` | `request_id`（可选，插件传自己的唯一值） | 200，返回同一把 Key 的**前缀**且不再返回明文 |
| `POST /usage/...` | 无（只读） | — |

### 1.4 错误码

| HTTP | code | 含义 | 插件应对动作 |
| --- | --- | --- | --- |
| 200 | 0 | 成功 | — |
| 400 | 40000 | 参数错误 | 展示 `msg`，不回滚（多半是配置错） |
| 400 | 40001 | **缺少幂等凭证**（`ref_type`/`ref_id` 为空） | 这是编码错误，直接报错 |
| 401 | 40100 | 签名失败 | 报错并告警（疑似密钥错） |
| 401 | 40101 | 时间戳超出窗口 | 校时后重试一次 |
| 401 | 40102 | nonce 重复（重放） | **视为已处理**，不要重投 |
| 403 | 40300 | app_id 被禁用 | 告警 |
| 404 | 40400 | 资源不存在 | 按不存在处理 |
| 409 | 40900 | 幂等冲突（同凭证不同参数） | 记录差异，人工核对 |
| 402 | 40200 | 额度不足（不允许负额度时） | 展示"到账失败：账户已异常" |
| 429 | 42900 | 超出内部接口限流 | 退避重试 |
| 500 | 50000 | 网关内部错误 | 进重试队列 |
| 503 | 50300 | 功能开关关闭 | 停止重试，告警 |

> **重试策略（插件侧 `_pushlog`）**：`4xx`（除 429）→ `done`，不重试；`429`/`5xx`/超时/网络错误 → 指数退避重试（1min, 2, 4, 8, 16, 30, 30…），最多 12 次后置 `failed` 并告警。

---

## 2. 接口清单

### 2.1 `GET /internal/v1/health`

免签名（仍带 header 时也接受）。用于模块 `testConnect()` 与连通性探测。

**响应 `data`**

```json
{
  "status": "ok",
  "version": "1.0.0",
  "time": 1762300000,
  "db": "ok",
  "cache": "ok",
  "queue": "ok",
  "flags": { "gateway_enabled": true, "billing_enforced": true }
}
```

### 2.2 `POST /internal/v1/accounts/sync`

创建或更新账户（按 `(site, v10_client_id)` upsert）。开通订单前插件先调它。

**请求**

```json
{ "v10_client_id": 1234, "email": "a@b.com", "status": 1 }
```

**响应 `data`**

```json
{ "account_id": 77, "v10_client_id": 1234, "status": 1, "balance_micros": 0 }
```

### 2.3 `POST /internal/v1/credit` ★ 核心

加/扣**余额额度**。`amount_micros` 可为负。**必须**带 `ref_type` + `ref_id`。

**请求**

```json
{
  "v10_client_id": 1234,
  "kind": "v10_order",
  "amount_micros": 100000000,
  "ref_type": "v10_order",
  "ref_id": "ORD20261105001",
  "remark": "余额充值 100 元",
  "allow_negative": false
}
```

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `v10_client_id` | ✅ | V10 客户 ID |
| `kind` | ✅ | `v10_order` \| `redeem_code` \| `admin_adjust` \| `refund` |
| `amount_micros` | ✅ | 微元；1 元 = 1_000_000；正数入账、负数出账 |
| `ref_type` | ✅ | 幂等凭证类型（自由字符串，建议用 `kind` 同名或更细的 `v10_order`/`redeem_code`/`admin_adjust`/`refund`） |
| `ref_id` | ✅ | 幂等凭证 ID（订单号/卡密ID/调整单号/退款单号） |
| `remark` | ❌ | 备注，进账本 |
| `allow_negative` | ❌ | 扣减时允许余额为负（退款场景默认 `true`，加额为 `false`） |

**响应 `data`**

```json
{ "applied": true, "account_id": 77, "balance_micros": 100000000, "ledger_entry_id": 501 }
```

`applied=false` 表示该凭证**此前已处理**（幂等命中），此时 `balance_micros` 是当前余额。

### 2.4 `POST /internal/v1/keys`

创建 Key，**明文只返回这一次**。

**请求**

```json
{
  "v10_client_id": 1234,
  "subscription_id": 9,
  "name": "生产环境",
  "models": ["deepseek-chat","gpt-4o-mini"],
  "ip_whitelist": ["1.2.3.0/24"],
  "expires_at": 1793836800,
  "request_id": "k-req-20261105-0001"
}
```

**响应 `data`**

```json
{
  "key_id": 321,
  "key": "sk-abcdEFGH...43位",
  "key_prefix": "sk-abcdEFGH",
  "name": "生产环境",
  "status": "active",
  "created_at": 1762300000
}
```

> 幂等：带 `request_id` 且重复调用时返回 `"key": null`（不再泄露明文），其余字段相同。
> 上限：每个 subscription 最多 `plan.max_keys`（默认 5）把 Key，超出返回 `400/40000`。

### 2.5 `GET /internal/v1/keys?v10_client_id=1234[&subscription_id=9]`

**响应 `data`**

```json
{
  "total": 2,
  "items": [
    {
      "key_id": 321, "key_prefix": "sk-abcdEFGH", "name": "生产环境",
      "status": "active", "subscription_id": 9, "v10_host_id": 555,
      "models": ["deepseek-chat"], "ip_whitelist": [],
      "expires_at": null, "last_used_at": 1762299000, "created_at": 1762200000,
      "usage_micros_30d": 1234567, "requests_30d": 42
    }
  ]
}
```

### 2.6 `PATCH /internal/v1/keys/{id}`

部分更新。请求体只带需要改的字段：`name`、`status`（`active`\|`suspended`）、`models`、`ip_whitelist`、`expires_at`。

**响应 `data`** 同 2.5 的单项结构。

### 2.7 `DELETE /internal/v1/keys/{id}`

吊销（软删除，`status=revoked`）。**幂等**：已吊销再调返回 200。

**响应 `data`**：`{"key_id": 321, "status": "revoked"}`

### 2.8 `GET /internal/v1/accounts/{client_id}/quota`

**响应 `data`**（套餐 + 余额两桶）

```json
{
  "account_id": 77,
  "v10_client_id": 1234,
  "balance_micros": 50000000,
  "status": 1,
  "subscriptions": [
    {
      "subscription_id": 9, "v10_host_id": 555, "plan_code": "pro",
      "status": "active", "billing_mode": "hybrid",
      "period_start": 1761945600, "period_end": 1764547200,
      "quota_total": 10000000, "quota_used": 2500000, "quota_remaining": 7500000
    }
  ]
}
```

### 2.9 `GET /internal/v1/models`

返回**可售模型目录**。插件用它渲染前台模型列表，并按 `plan.model_group` 过滤。

**响应 `data`**

```json
{
  "total": 1,
  "items": [
    {
      "model_name": "deepseek-chat", "display_name": "DeepSeek Chat",
      "group_name": "default", "context_len": 65536,
      "capabilities": ["tools","json"], "status": 1,
      "ratio": { "model_ratio": 0.5, "completion_ratio": 2.0, "cached_ratio": 0.5 },
      "price_hint": { "input_micros_per_1m": 500000, "output_micros_per_1m": 1000000 }
    }
  ]
}
```

### 2.10 `GET /internal/v1/usage/summary`

查询参数：`v10_client_id`（必填）、`v10_host_id`、`api_key_id`、`from`、`to`（unix 秒）、`group_by`（`day`\|`model`\|`key`\|`host`，默认 `day`）。

**响应 `data`**

```json
{
  "totals": { "requests": 120, "prompt_tokens": 90000, "completion_tokens": 30000, "cost_micros": 4500000 },
  "buckets": [
    { "key": "2026-11-05", "requests": 20, "prompt_tokens": 15000, "completion_tokens": 5000, "cost_micros": 750000 }
  ]
}
```

### 2.11 `GET /internal/v1/usage/logs`

分页明细。**不返回 prompt/response 正文**（合规）。参数：`v10_client_id`（必填）、`api_key_id`、`model`、`from`、`to`、`page`（从 1）、`page_size`（≤200，默认 20）。

**响应 `data`**

```json
{
  "total": 120, "page": 1, "page_size": 20,
  "items": [
    {
      "request_id": "01JC8Z...", "model": "deepseek-chat",
      "prompt_tokens": 1200, "completion_tokens": 400, "cost_micros": 60000,
      "billing_mode": "subscription", "stream": true, "estimated": false,
      "status_code": 200, "latency_ms": 1830, "ttft_ms": 240,
      "api_key_id": 321, "v10_host_id": 555, "created_at": 1762300000
    }
  ]
}
```

### 2.12 `POST /internal/v1/subscriptions` ★ 开通/续费

按 `v10_host_id` 幂等。开通与续费是同一接口：**同一 host 再次调用即为续费/变更**（幂等键 = `v10_host_id`，续费幂等键 = 目标 `period_end`）。

**请求**

```json
{
  "v10_client_id": 1234,
  "v10_host_id": 555,
  "plan_code": "pro",
  "period": "month",
  "quota_tokens": 10000000,
  "billing_mode": "hybrid",
  "period_start": 1761945600,
  "period_end": 1764547200,
  "max_keys": 5
}
```

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `v10_client_id` | ✅ | |
| `v10_host_id` | ✅ | 幂等键 |
| `plan_code` | ✅ | 必须在 `cz_plan` 存在（不存在则 `404/40400`） |
| `period` | ❌ | `month`\|`quarter`\|`year`\|`once`，缺省用 plan 的值 |
| `quota_tokens` | ❌ | 覆盖 plan 的周期额度 |
| `billing_mode` | ❌ | 缺省用 plan 或插件默认 |
| `period_start`/`period_end` | ❌ | 缺省按 `period` 从当前时间推算 |
| `max_keys` | ❌ | 覆盖 plan 的 Key 上限 |

**响应 `data`**

```json
{
  "subscription_id": 9, "v10_host_id": 555, "created": true, "renewed": false,
  "status": "active", "plan_code": "pro",
  "period_start": 1761945600, "period_end": 1764547200,
  "quota_total": 10000000, "quota_used": 0, "quota_remaining": 10000000
}
```

> 语义：**新建**时 `created=true`；**同 host 再次调用且目标周期与当前不同** → 续费，`renewed=true` 且 `quota_used` 归零；**目标周期与当前相同** → 幂等命中，`created=false, renewed=false`，数据不变。

### 2.13 `POST /internal/v1/subscriptions/{id}/suspend`

请求：`{ "reason": "overdue", "suspend_type": "overdue" }`
效果：订阅 `status=suspended`，其下所有 Key 置 `suspended`（推理返回 403 `key_suspended`）。
**响应 `data`**：`{"subscription_id":9,"status":"suspended","keys_affected":2}`

### 2.14 `POST /internal/v1/subscriptions/{id}/resume`

效果：订阅 `status=active`，其下 `suspended` 的 Key 恢复 `active`（**不恢复已吊销的**）。
**响应 `data`**：`{"subscription_id":9,"status":"active","keys_affected":2}`

### 2.15 `DELETE /internal/v1/subscriptions/{id}`

终止回收：订阅 `status=terminated`，**吊销**其下全部 Key（`revoked`），用量归档保留。
**响应 `data`**：`{"subscription_id":9,"status":"terminated","keys_revoked":2}`

### 2.16 `GET /internal/v1/subscriptions/{id}`

**响应 `data`** 同 2.12。

### 2.17 `GET /internal/v1/billing/host-usage`

按量计费取数（对应 V10 `get_host_flow` hook）。参数：`v10_host_id`（必填）、`from`、`to`（unix 秒）。

**响应 `data`**

```json
{
  "v10_host_id": 555,
  "from": 1761945600, "to": 1764547200,
  "requests": 1200, "prompt_tokens": 900000, "completion_tokens": 300000,
  "cost_micros": 45000000, "currency": "CNY", "amount": "45.000000"
}
```

> `amount` 是给 V10 出账用的**十进制字符串**（避免浮点），保留 6 位小数，单位元。

---

## 3. PHP 侧签名参考实现（与 Go 逐字节一致）

```php
function cz_sign(string $secret, string $method, string $path, string $sortedQuery,
                 string $body, int $timestamp, string $nonce): string {
    $raw = implode("\n", [
        strtoupper($method),
        $path,
        $sortedQuery,                       // 无查询串时为空字符串
        hash('sha256', $body),              // 空体 = hash('sha256','')
        (string)$timestamp,
        $nonce,
    ]);
    return hash_hmac('sha256', $raw, $secret);
}

// 查询串排序：按 key 升序（ASCII），值用 rawurlencode
function cz_canonical_query(array $q): string {
    ksort($q, SORT_STRING);
    $parts = [];
    foreach ($q as $k => $v) {
        $parts[] = rawurlencode((string)$k) . '=' . rawurlencode((string)$v);
    }
    return implode('&', $parts);
}
```

> ⚠️ Go 侧用 `url.QueryEscape`（空格 → `+`），PHP 用 `rawurlencode`（空格 → `%20`）。
> **目前所有 `/internal/v1` 查询串都不含空格**，因此两者等价。若将来出现空格参数，统一改用 `%20`（Go 侧将切换到自定义编码器）。

---

## 4. 不在本契约内（明确边界）

- 网关 → V10 的**反向回调**：本版不做。额度耗尽由网关在推理响应中返回 402，V10 侧靠 `usage/summary` 轮询展示。
- 支付/退款/卡密：全部在 V10 内部完成，网关只有 §2.3 的 `credit`。
- 用户登录/注册/实名：V10 内部。
