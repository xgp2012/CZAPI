# `/internal/v10` 接口契约

> 本文件是该网关与魔方业务系统 V10 之间**唯一的**服务端契约。
> 变更这里之前先想清楚：V10 插件是独立部署的，网关升级不会自动升级插件。

## 边界

| 归属 | 负责什么 | 不负责什么 |
| --- | --- | --- |
| V10 | 商品、订单、支付、发票、客户主数据、Key 的可视化 | 不碰计量、不碰余额 |
| 网关 | 鉴权、模型转发、计费、额度账本、Key 存储 | **一分钱都不经手** |

推论：

- 网关没有注册、没有密码登录、没有充值入口。这些开关在启动时被强制关闭（`internalv10.bootstrap.go`）。
- 加额度只有一条路径：V10 通知网关「这个客户 +N 微元」。网关只做记账。
- V10 客户没有网关密码，进控制台一律走一次性 SSO 票据。

## 认证

除 `GET /health` 外，全部接口要求四个请求头：

```
X-V10-App-Id:   <站点 app_id>
X-V10-Timestamp: <Unix 秒>
X-V10-Nonce:     <每次请求唯一，建议 32 位十六进制>
X-V10-Signature: <hex(HMAC-SHA256(app_secret, stringToSign))>
```

`stringToSign` 是六行，用 `\n` 连接，**结尾不加换行**：

```
METHOD
PATH
SORTED_QUERY
SHA256_HEX_BODY
TIMESTAMP
NONCE
```

细则：

- `METHOD` 大写。
- `PATH` 是完整路径，含 `/internal/v10` 前缀，不含域名。
- `SORTED_QUERY` 按 key 升序拼接 `key=value`，用 `&` 连接；**空查询串就留空行**。
  百分号编码必须用 `%20` 表示空格，不能用 `+`（PHP 的 `rawurlencode` 与 Go 侧实现一致）。
- `SHA256_HEX_BODY` 是请求体原始字节的 SHA-256 小写十六进制；
  GET 无请求体时是空串的哈希，即 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`。
- 时间戳偏差超过 **5 分钟**拒绝（`40101`）。
- nonce 在 5 分钟窗口内不可重复（`40102`）。

## 金额单位

**微元（micros）**：1 元 = `1_000_000` 微元。所有金额字段都以微元传输，整数，无小数。

网关内部额度单位是 new-api 的 quota。换算：

```go
quota = amountMicros * common.QuotaPerUnit / 1_000_000
```

**先乘后除**。先除会把不足 1 元的零头截断——2.5 元会被记成 2 元，是真金白银的损失。

## 响应信封

所有签名接口统一返回：

```json
{ "code": 0, "msg": "ok", "data": { } }
```

`code == 0` 为成功，其余为失败。HTTP 状态码同时表达语义，两者都要看。

## 错误码

| code | HTTP | 含义 | 插件应如何处理 |
| --- | --- | --- | --- |
| `0` | 200 | 成功 | — |
| `40000` | 400 | 参数错误 | 不重试，修参数 |
| `40001` | 400 | 缺 `ref_type` / `ref_id` | 不重试，修调用 |
| `40100` | 401 | 签名不匹配 | 不重试，检查 secret |
| `40101` | 401 | 时间戳超窗 | 重试前先校时 |
| `40102` | 401 | nonce 重放 | 换新 nonce 重试 |
| `40200` | 400 | 余额不足（`allow_negative=false` 时） | 不重试 |
| `40300` | 403 | 站点被停用 | 不重试，联系运维 |
| `40400` | 404 | 对象不存在 | 视业务而定 |
| `40900` | 409 | 同一 ref 的金额/类型冲突 | 不重试，人工核账 |
| `42900` | 429 | 限流 | 退避重试 |
| `50000` | 500 | 网关内部错误 | 退避重试 |
| `50300` | 503 | 功能未启用 | 不重试 |

重试策略：

- `40000/40001/40100/40101/40200/40300/40900/50300` → 标记完成，不重试。
  其中 `40101`/`40102` 是**例外**：它们说明这次请求根本没被处理，换新 nonce 重发一次是安全的。
- `42900/50000`、超时、网络错误 → 指数退避 `1, 2, 4, 8, 16, 30, 30…` 秒，最多 12 次。

## 接口清单

### `GET /health`（免签名）

配置页"测试连接"用。返回进程存活与协议版本。

### `POST /accounts/sync`

确保 `(站点, client_id)` 对应的网关账户存在。

```json
{ "client_id": 2001, "display_name": "张三", "email": "z@example.com" }
```

- `client_id` 必填且为正整数。
- `display_name` / `email` 可选。**显示名允许重复**，网关用户名由 `(app_id, client_id)` 派生，
  不受显示名影响。邮箱与既有用户冲突时会被静默丢弃（V10 客户不靠邮箱登录）。
- 幂等：重复调用返回同一账户。

### `GET /accounts/quota?client_id=`

返回余额与订阅摘要。

```json
{
  "user_id": 12, "client_id": 2001,
  "balance_quota": 1250000, "balance_micros": 2500000, "balance_amount": "2.500000",
  "used_quota": 0, "used_micros": 0, "request_count": 0,
  "status": 1, "group": "default",
  "subscriptions": [
    { "host_id": 90001, "user_subscription_id": 3, "plan_id": 1, "plan_title": "基础版",
      "host_status": "active", "quota_total": 50000000, "quota_used": 0, "quota_remaining": 50000000,
      "start_time": 0, "end_time": 0, "next_reset_time": 0, "status": "active" }
  ]
}
```

### `POST /credit`

唯一的加/扣额度入口。

```json
{
  "client_id": 2001,
  "kind": "v10_order",
  "amount_micros": 2500000,
  "ref_type": "v10_order",
  "ref_id": "ORDER-20261105-001",
  "remark": "订单支付",
  "allow_negative": false
}
```

- `kind` ∈ `v10_order` | `redeem_code` | `admin_adjust` | `refund`，仅用于审计分类。
- `ref_type` + `ref_id` 必填，是**幂等凭证**。网关对 `(site_id, ref_type, ref_id)` 建了唯一索引，
  同一对值重复提交只生效一次，第二次返回 `applied: false` 并回放首次的余额。
- `allow_negative` 默认 `false`。扣款时若余额不足返回 `40200`；设为 `true` 才允许透支。
- 金额可以为负（扣款）。`amount_micros: 0` 会被记录但不改余额。

响应：

```json
{ "applied": true, "user_id": 12, "client_id": 2001, "delta_quota": 1250000,
  "balance_after": 1250000, "ref_id": "ORDER-20261105-001" }
```

### `POST /keys`

```json
{ "client_id": 2001, "host_id": 90001, "name": "生产", "models": ["gpt-4o-mini"],
  "ip_whitelist": ["1.2.3.4"], "expires_at": 1790000000, "max_keys": 5 }
```

明文 Key **只在这一次响应里出现**，之后任何接口都不再返回。
可达上限 `max_keys`（0 表示用站点默认值），超限返回 `40000`。

### `GET /keys?client_id=&host_id=`

永不含明文，只给前缀。

### `PATCH /keys/:id`

```json
{ "client_id": 2001, "name": "改名", "status": "active|suspended|revoked",
  "models": ["gpt-4o-mini"], "ip_whitelist": [], "expires_at": 0 }
```

`client_id` 必填：网关用它校验 Key 归属，防止越权修改他人 Key。

### `DELETE /keys/:id?client_id=`

吊销。**幂等**：已吊销再调一次仍返回成功。
吊销是永久的（内部置 `expired`），套餐恢复不会把它复活。

### `GET /models`

返回 `{ "items": [{ "name": "gpt-4o-mini" }, ...] }`，仅含网关当前启用的模型。

### `GET /usage/summary`

查询参数：`client_id`（必填）、`from`、`to`（Unix 秒，缺省为最近 30 天）、`group_by` ∈ `day|model|token`。

```json
{
  "user_id": 12, "client_id": 2001, "from": 0, "to": 0, "group_by": "day",
  "totals": { "request_count": 0, "quota": 0, "quota_micros": 0, "quota_amount": "0.000000",
              "prompt_tokens": 0, "completion_tokens": 0 },
  "buckets": [ { "key": "2026-11-05", "request_count": 0, "quota_micros": 0, "...": 0 } ]
}
```

`day` 分桶按日期升序；`model` / `token` 分桶按消耗额度降序。

聚合在 Go 侧完成而非 SQL，是为了同时兼容 SQLite / MySQL / PostgreSQL
（避免 `FROM_UNIXTIME`、`DATE_FORMAT` 这类方言函数）。

### `GET /usage/logs`

查询参数：`client_id`（必填）、`from`、`to`、`page`（默认 1）、`page_size`（默认 20，上限 200）、`model`。

返回条目**故意不含**请求正文、IP、渠道 ID——账单页面不需要，泄露了反而增加风险。

### `POST /hosts`

V10 商品实例（host）的开通与续期。

```json
{ "client_id": 2001, "host_id": 90001, "plan_id": 1, "duration_days": 30,
  "ref_type": "v10_host_open", "ref_id": "OPEN-90001" }
```

- `plan_id` 是网关侧 `subscription_plans.id`，正整数。
- `ref_type` + `ref_id` 必填，是**幂等凭证**。同一对值重放时不会再次续期，
  而是直接返回 host 的当前状态。
  这条不能省：V10 的请求超时后会自动重试，而超时的那一次往往**已经成功**了；
  不去重就会再顺延一个周期，等于白送客户一段时间。
- 同一 `host_id` 换一个**新凭证**调用是**续期**，不是新建：到期时间从**现有到期时间**
  往后顺延，已过期才从当前时间起算。从当前时间重算会让客户提前续费反而少拿时间。
- `host` 已绑定到别的账户时返回 `40000`，不允许跨客户抢占。

### `POST /hosts/:id/status`

```json
{ "client_id": 2001, "status": "active|suspended|terminated", "reason": "欠费" }
```

状态联动 Key：

| host 状态 | Key 状态 |
| --- | --- |
| `active` | 被停用的 Key 恢复启用；已吊销的不动 |
| `suspended` | 启用的 Key 置为停用（可恢复） |
| `terminated` | 启用的 Key 全部永久吊销 |

恢复时只拉回"被停用"的 Key，是为了不让一次暂停恢复把客户早就作废的 Key 全部复活。

### `GET /hosts/:id/status?client_id=`

单个 host 的当前状态。

### `POST /sso/ticket`

```json
{ "client_id": 2001 }
```

返回：

```json
{ "ticket": "<一次性令牌>", "login_url": "https://console.example.com/v10/login#<ticket>",
  "expires_at": 1790000060 }
```

票据经 **URL fragment** 传递，不进服务端访问日志。
有效期 **60 秒**，只能使用一次，库中只存 SHA-256 哈希。

## 浏览器侧接口

### `POST /api/v10/sso/exchange`（免签名）

调用方是浏览器，无法持有 `app_secret`，因此这个接口不签名。
安全性完全来自票据本身的三个性质：一次性、60 秒过期、哈希存储。

```json
{ "ticket": "<票据>", "app_id": "v10" }
```

成功返回 `access_token`、`session` 与用户信息，并写入 refresh cookie，
响应带 `Cache-Control: no-store`。失败返回 `success: false`。

## 运维接口（管理员会话）

挂在 `/api/v10/admin` 下，需管理员登录，V10 客户访问不到。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/sites` | 站点列表（不含 secret 明文） |
| POST | `/sites` | 注册站点，明文 secret 只返回一次 |
| POST | `/sites/:id/secret` | 轮换密钥，旧密钥**立即**失效 |
| POST | `/sites/:id/status` | 启用/停用站点 |
| GET | `/site-options` | 站点 + 可选套餐 + `quota_per_unit` |

`quota_per_unit`（默认 `500000`）给前端换算用，避免两边各写一套换算逻辑。
