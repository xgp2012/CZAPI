# V10 插件开发文档

面向**修改这个插件的人**。安装与运维看 [`README.md`](README.md)，接口契约看
[`../docs/internal-v10-contract.md`](../docs/internal-v10-contract.md)。

本文只回答一个问题：**动手改之前，哪些东西不知道就会踩坑。**

---

## 1. 两份交付物，职责不重叠

| 交付物 | 部署位置 | 负责 |
| --- | --- | --- |
| `idcsmart_llm_gateway/`（插件） | `public/plugins/addon/` | 客户自助面（Key / 用量 / 卡密）、后台运营面、与网关通信的全部客户端 |
| `llm_gateway/`（模块） | `public/plugins/server/` | 商品生命周期：开通 / 暂停 / 恢复 / 续费 / 终止 / 按量取数 |

**唯一共享的代码是 `idcsmart_llm_gateway/model/LlmApi.php`。** 模块通过
`requireApiClient()` 动态引入它，签名逻辑全局只有一份。改签名不要在两个地方改。

因此**插件必须先装、先配**：模块读的是插件的 `config.php` + `idcsmart_plugin.config`，
模块自己没有 `app_id` / `app_secret` 配置项。

```
V10 订单支付成功
   ├─ 充值/余额类订单 → 插件 hooks.php(order_paid) → LlmApi::onOrderPaid
   │                     → POST /internal/v10/credit → 网关加余额
   └─ 套餐类订单     → 模块 createAccount/renew → POST /internal/v10/hosts → 网关绑订阅
```

两条路**互斥**：`onOrderPaid` 见到 `order.host_id > 0` 直接跳过，把套餐订单让给模块。
否则同一笔钱会同时加成余额和套餐额度。

---

## 2. 调用链：一次加额要穿过四道闸门

理解这条链是理解整个插件的前提。以"客户充 100 元"为例：

```
① V10 订单支付成功
       ↓  hooks.php  add_hook('order_paid')
② LlmApi::onOrderPaid($orderId)
       ↓  TxnModel::claim()          ← 本地闸门：uk_kind_ref(kind, ref_no)
③ LlmApi::creditAccount() → LlmApi::deliver()
       ↓  PushLogModel::enqueue()    ← 落库在先，请求在后（顺序不能反）
       ↓  HTTP POST /internal/v10/credit
④ 网关 v10_refs (site_id, ref_type, ref_id) 唯一索引  ← 远端闸门
```

四道闸门的意义各不相同，**拆掉任何一道都会漏钱或重复加钱**：

| 闸门 | 位置 | 挡住什么 |
| --- | --- | --- |
| `TxnModel` 唯一键 | V10 本地 | 同一订单被 hook 触发两次 |
| `pushlog` 先落库 | V10 本地 | 请求发出后进程被杀，钱丢了没记录 |
| `v10_refs` 唯一索引 | 网关 | 重投时重复入账 |
| `classify()` 判定 | V10 本地 | 把"可重试"当"已失败"，或反之 |

**顺序不能反**：`deliver()` 必须 `enqueue()` 之后再发请求。先发后记，进程在中间被杀就是永久丢单。

### `classify()` 的四种判定

改这个函数前先读网关 `internalv10/middleware.go` 的错误码定义。

| 判定 | 触发 | 后续动作 |
| --- | --- | --- |
| `ok` | `code=0` 且 HTTP < 400 | `pushlog` 置 done |
| `done` | 4xx（除 429）、`40102` nonce 重放 | 置 done，差异写进 `last_error` |
| `retry` | 429 / 5xx / 网络错误 / `40101` 时钟超窗 | 指数退避，最多 12 次 |
| `stop` | `50300` 功能开关关闭 | 立即终止，不再重试 |

`40102` 判成 `done` 而不是 `retry` 是刻意的：nonce 重放说明**网关已经处理过这次请求**，
再重试只会一直拿到 40102，队列永远清不掉。

---

## 3. 数据模型：五张表，各有各的真相来源

表名前缀 `idcsmart_addon_idcsmart_llm_gateway_`（V10 自动补 `idcsmart_`）。

| 表 | 是不是事实来源 | 关键约束 |
| --- | --- | --- |
| `key` | ❌ 镜像 | `uk_key_id` 全局唯一 |
| `usage` | ❌ 快照 | `uk_client_host_key_day` |
| `txn` | ✅ 本地幂等凭证 | `uk_kind_ref(kind, ref_no)` |
| `pushlog` | ✅ 投递状态 | `idx_status_next(status, next_retry_at)` |
| `redeem` | ✅ 卡密本体 | `uk_code_hash` |

**网关是 Key、额度、用量的事实来源。** 本地 `key` / `usage` 只服务于两件事：
后台检索、网关不可达时的只读降级展示。**不要拿它们做扣费或权限判断。**

### 3.1 `key.host_id` 是命名约定的投影，不是独立事实

网关的 `token` 表（new-api 上游）**没有 host 列**。host 归属编码在 `token.Name` 的
`#<hostId>` 后缀里，由 `internalv10/key.go` 的 `keyName()` 生成、`hostIdFromName()` 解析。

这带来三个必须遵守的纪律：

1. **改名必须带原后缀**。`UpdateKey` 里写的是 `keyName(新名, hostIdFromName(老名))`，
   不是直接赋值。直接赋值会让这把 Key 从"产品 A 的 Key"悄悄变成"不属于任何产品的 Key"。
2. **`syncFromGateway()` 绝不主动清零**。网关没返回 `host_id`（或返回 0）时保留本地值。
   早期版本无条件覆盖，结果每刷新一次列表就把所有归属抹掉一次。
3. **不能靠 SQL `LIKE` 过滤**。`LIKE '#9%'` 会误伤 `#90`，所以 `ListKeys` 的 host 过滤在内存里做。

### 3.2 `usage` 表的列名与网关响应**不同名**

这是最容易踩的坑：

| 网关 `UsageBucket` 字段 | 本地 `usage` 列 |
| --- | --- |
| `request_count` | `requests` |
| `quota_micros` | `cost_micros` |
| `prompt_tokens` | `prompt_tokens` |
| `completion_tokens` | `completion_tokens` |

`UsageModel::upsertDay()` 负责这个映射。早期版本直接读 `$bucket['requests']`，
而网关响应里根本没有这个键，于是**每天的快照都被写成 0**——网关一旦不可达，
前台"近 30 天用量"就是一片零。现在两种键名都接受。

### 3.3 `pushlog.method` 记录重投用的 HTTP 方法

早期重投写死 POST，非 POST 的投递失败后永远重投不成功。现在 `enqueue()` 落库时
一并记下 method，`retryFailed()` / `retryOne()` 用 `methodOf($row)` 取回，老数据回落到 POST。

---

## 4. 配置读取

`LlmGatewayLogic::config()` = `config.php` 的文件默认值 + `idcsmart_plugin.config` 的 JSON 覆盖。
`config.php` 里每项是 `['title','type','value','tip']`，`value` 是默认值。

同一次请求内只读一次 DB（静态缓存）。**改完配置要 `saveConfig()` 或重启请求**，
否则同一请求内的后续读取仍是旧值。

新增配置项的步骤：

1. `config.php` 加一项（含 `value` 默认值，不能只写 title）
2. 三套语言包 `lang/{zh-cn,zh-hk,en-us}.php` 补 `llm_gw_*` 键
3. 用 `configInt()` / `config()` 读取，不要直接摸 `$config['x']`

---

## 5. 路由与鉴权：三组路由，鉴权强度完全不同

见 `route.php`。**这是全插件最危险的地方**，务必分清：

| 组 | 中间件 | 含义 | 补充要求 |
| --- | --- | --- | --- |
| `console/v1/api` | `ParamFilter` + `Check` | **免登录** | 每个方法第一行必须 `CallbackGuard::verify()` |
| `console/v1` | `+ CheckHome` + `RejectRepeatRequest` | 会员登录态 | 用 `get_client_id()` 取客户 |
| `{DIR_ADMIN}/v1` | `+ CheckAdmin` | 管理员 | 控制器继承 `PluginAdminBaseController` |

**`Check` 中间件只表示"不校验登录态"，不代表已鉴权。** 把业务方法放进免登录组
又忘了验签，等于把它裸奔在公网上。

`CallbackGuard` 用**独立的 `callback_secret`**，不用 `app_secret`——后者泄露时
仍然无法伪造"网关 → V10"的回调。未配置 `callback_secret` 时该组一律拒绝。

> 该回调协议目前是**预留**状态：网关侧尚未实现反向回调，`quotaExhausted` 只落审计。
> 要真正启用，需要先在网关侧实现对应的签名发送逻辑。

---

## 6. 金额与单位

- 传输一律**微元整数**（1 元 = 1,000,000）
- 元 → 微元：`LlmGatewayLogic::toMicros()`，内部 `round(x * 1e6)`
- 微元 → 元字符串：`fromMicros()`，保留 6 位小数

**不要在前端或模板里自己做换算**，更不要用浮点运算比大小。展示统一走 `fromMicros()`。

---

## 7. 幂等凭证：最容易被改坏的一处

`ref_type` + `ref_id` 是网关唯一索引的键。规则：

| 场景 | ref_type | ref_id |
| --- | --- | --- |
| 充值/调账 | `v10_order` | 订单号 |
| 卡密 | `redeem_code` | 卡密表主键 ID |
| 开通 | `v10_host_open` | 优先 `order_id` |
| 续费 | `v10_host_renew` | 优先 `order_id`，其次 `RENEW-{host_id}-{due_time}` |

**绝对不能用 `time()` 或任何每次调用都会变的值。** 那样每次重试都是新凭证，
幂等直接失效，客户白拿时间或白拿额度。

`renew()` 在这件事上做得最细，注释值得一读：取不到稳定凭证时**直接报错**，
而不是编一个假凭证——假凭证的后果是客户第二次续费被静默挡掉（付了钱没拿到时间），
比报错难查得多。

---

## 8. 改代码时的检查清单

### 改网关客户端（`LlmApi.php`）

- [ ] 签名字段、请求头、路径前缀有没有动？动了必须同步 `gateway/internalv10/sign.go`
- [ ] 新接口是否走 `deliver()`？只有"改变账本"的调用才需要落库重投
- [ ] 返回值是否区分了 `code` 与 `http_status`？两者语义不同，`code=0` 但 HTTP 500 也见过
- [ ] 空请求体的哈希是 `sha256("")`，别把 `''` 和 `'{}'` 混用

### 改 Key 相关

- [ ] 有没有破坏 `#<hostId>` 后缀？
- [ ] `syncFromGateway()` 会不会把本地 `host_id` 清零？
- [ ] `suspended` 与 `revoked` 是否仍然分离？两者映射成同一个状态会让"吊销"被套餐恢复复活

### 改表结构

- [ ] `schemaSql()` 是 `CREATE TABLE IF NOT EXISTS`，**对已存在的表不生效**
- [ ] 新列必须同时写进 `addMissingColumns()`，否则已安装实例升级后仍缺列，
      每次写该表都抛 `Unknown column`
- [ ] 三个语言包都补了键？

### 改 hooks

- [ ] 事件名是否在目标实例的 Hook 列表里确认过？（见 §9）
- [ ] 回调参数结构是否做了兼容取值？

### 提交前

```bash
php -l <改动的每个文件>
```

本仓库没有 V10 实例，**PHP 语法检查是唯一的静态门槛**，务必逐个跑。

---

## 9. 事件名分级：哪些已验证，哪些是猜的

这是插件的**最大未知数**，改动前必须知道自己在依赖哪个级别：

| 事件 | 级别 | 说明 |
| --- | --- | --- |
| `five_minute_cron` | A · 源码实证 | 官方 ticket 插件有同样的登记 |
| `daily_cron` | A · 源码实证 | 注意不是 `day_cron` |
| `after_host_delete` | B · 文档示例 | 模块开发文档给出的 add_hook 示例 |
| `order_paid` | **C · 未经实例确认** | 逐页核对了 21 个订单/支付/退款 Hook，**没有**直接的"支付后"条目 |
| `get_host_flow` | B · 文档示例 | 按量计费取数，需在实例确认 |

**`order_paid` 是充值入账的唯一触发点。** 如果它在实例上不存在，充值时不会自动加额度。
排查顺序：

1. 看 V10 后台「Hook 列表」里有没有 `order_paid`
2. 没有的话，把 `hooks.php` 里的事件名换成实例上的真实名称（回调体不用改）
3. 临时兜底：在插件后台「推送队列」页手动补投
4. `five_minute_cron` 是否已登记并按时执行？（`install()` 会写 `idcsmart_plugin_hook`，
   但若该表列结构不同，插入会被 catch 掉且不阻断安装——此时定时任务根本不跑）

---

## 10. 客户侧计数与限流的局限

`RedeemController` 的"每人每日 5 次失败上限"存在 **V10 的 Cache**（ThinkPHP Cache facade），
键含日期，TTL 到当日 24 点。

- 用 Cache 而不是新建第 6 张表，是为了守住"五张表"的结构约定
- **若实例的 cache 驱动是 file 且多进程，该限流基本无效**
- 需要严格限流时，改为独立表 + 唯一键

同理，网关侧 nonce 去重是**进程内**的：多副本部署下同一 nonce 可能在两个副本各通过一次。
签名本身仍安全（nonce 只防重放，不防伪造）。

---

## 11. 已知限制

- **渠道管理不在 V10 后台。** 网关的渠道增删改需要管理员 JWT，与 `/internal/v10` 的
  HMAC 体系不同源。V10 后台只做只读状态核验。
- **推送队列是"至多一次有效"语义。** 网关幂等保证不会重复入账，但如果网关处理成功
  而响应丢失，V10 会一直以为失败并重投——重投是安全的，只是多一条"已幂等忽略"的记录。
- **卡密由 V10 生成、V10 兑换。** 网关的 `/api/user/topup` 对 V10 客户是关闭的。
- **卸载默认保留数据。** 需要彻底清理时把 `keep_data_on_uninstall` 关掉。

---

## 12. 修改记录

### 2.1.0（本次）

修复了九个会导致"用起来全是问题"的确定性缺陷，多数不报错，只是结果不对：

| # | 问题 | 影响 | 文件 |
| --- | --- | --- | --- |
| 1 | 用量快照读错字段名 | 快照全为 0，降级展示一片零 | `UsageModel.php` |
| 2 | `KeyView` 缺 `host_id` | 按产品筛选永远为空、删产品吊销不到 Key | `key.go` |
| 3 | 同步覆盖 `host_id` | 每刷新一次列表抹掉一次归属 | `KeyModel.php` |
| 4 | 改名丢 `#hostId` | Key 悄悄失去产品归属 | `key.go` |
| 5 | `revoked` 映射成 `disabled` | 已吊销的 Key 被套餐恢复复活 | `key.go` |
| 6 | `keys?host_id=` 被忽略 | 前台按产品筛选无效 | `router.go` |
| 7 | 重投写死 POST | 非 POST 投递永远重投不成功 | `LlmApi.php` / `PushLogModel.php` |
| 8 | `upgrade()` 补不上新列 | 已安装实例缺列，写表抛异常 | `IdcsmartLlmGateway.php` |
| 9 | 概览用客户名冒充 email | 前台显示错误的"邮箱" | `IndexController.php` |

**升级到 2.1.0 需要给已安装实例补一列**（`upgrade()` 会自动做，也可手工执行）：

```sql
ALTER TABLE `idcsmart_addon_idcsmart_llm_gateway_pushlog`
  ADD COLUMN `method` varchar(8) NOT NULL DEFAULT 'POST' COMMENT '重投时使用的HTTP方法';
```

---

## 13. 相关文档

- [插件安装与运维](README.md)
- [`/internal/v10` 接口契约](../docs/internal-v10-contract.md)
- [部署与验收](../docs/deployment.md)
- [项目总览](../README.md)
- 网关侧实现：`gateway/internalv10/`（`sign.go` 签名、`key.go` Key、`credit.go` 账本、`usage.go` 用量）
