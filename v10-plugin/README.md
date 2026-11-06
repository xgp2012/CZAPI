# CZAPI × 魔方业务系统 V10 集成包

本目录是「CZAPI LLM 网关」与「魔方业务系统 V10（idcsmart）」之间的**全部 PHP 交付物**。

适用：魔方业务系统 V10（开发文档基线 V10.5.6）+ PHP 8.0 及以上（在 PHP 8.3 通过语法检查）。

> 网关侧是 new-api 的二开分支（AGPL-3.0），源码在 [`../gateway`](../gateway)，
> 接口契约见 [`../docs/internal-v10-contract.md`](../docs/internal-v10-contract.md)，
> 部署步骤见 [`../docs/deployment.md`](../docs/deployment.md)。
> **本文档只讲 V10 这边的安装与运维。**
>
> **要改这个插件的代码，先读 [`DEVELOPMENT.md`](DEVELOPMENT.md)** ——
> 那里写了调用链、四道幂等闸门、`host_id` 命名约定、事件名分级和改代码检查清单。

---

## 目录

1. [这套东西的分工](#1-这套东西的分工)
2. [交付物速览](#2-交付物速览)
3. [钱为什么不过网关](#3-钱为什么不过网关)
4. [安装前准备](#4-安装前准备)
5. [安装步骤](#5-安装步骤)
6. [配置说明](#6-配置说明)
7. [商品与套餐映射](#7-商品与套餐映射)
8. [日常运维](#8-日常运维)
9. [失败重投队列](#9-失败重投队列)
10. [故障排查](#10-故障排查)
11. [待实例验证清单](#11-待实例验证清单)
12. [已知限制](#12-已知限制)

---

## 1. 这套东西的分工

一句话：**V10 是唯一的用户入口和唯一的收钱地方；网关只记账、只转发、只计量。**

```
用户 ──浏览器──> V10（注册 / 下单 / 付款 / 开票 / 退款 / 订阅管理）
                   │
                   │  iframe 嵌入（SSO 票据换会话）
                   ▼
              网关控制台（Key 管理、用量看板）——客户看到的"网关"其实是 V10 的插件页面

V10 服务端 ──HMAC 签名──> 网关 /internal/v10/*（加额度、开订阅、查用量）
```

这条边界是**结构性的**，不是靠配置开关：

- 网关启动时会强制关闭面向最终用户的注册、密码注册、邮箱验证与自助充值。
- 网关还额外做身份判定：凡由 V10 托管的账户，一律禁止使用网关自带的
  付费、卡密兑换、订阅自购入口（包括用网关余额支付订阅）。
  这层拦截见 `gateway/internalv10/guard.go`，由验收套件第 5 项守护。
- 客户在网关上没有可用密码（存的是占位串），只能走 V10 签发的 SSO 票据登录。

## 2. 交付物速览

| 组件 | 源码目录 | 部署到 | 作用 |
| --- | --- | --- | --- |
| 插件 | `idcsmart_llm_gateway/` | `public/plugins/addon/idcsmart_llm_gateway/` | 用户自助面（Key / 用量 / 卡密）+ 后台运营面 + 与网关通信的全部客户端代码 |
| 模块 | `llm_gateway/` | `public/plugins/server/llm_gateway/` | 把 LLM 套餐做成 V10 可售卖商品：开通 / 暂停 / 恢复 / 删除 / 续费 |

**插件必须先装、先配。** 模块通过 `LlmGatewayLogic::config()` 读取插件的
`config.php`，不需要在模块里再填一遍 `app_id` / `app_secret`。

### 插件目录结构

```
idcsmart_llm_gateway/
├── IdcsmartLlmGateway.php          # 主类（install/uninstall/upgrade + 建表）
├── config.php                      # 后台配置表单（系统自动渲染）
├── route.php                       # 三组路由（免登录回调 / 会员中心 / 后台）
├── hooks.php                       # Hook 注册（订单支付、产品删除、每日/五分钟任务）
├── auth.php / auth_clientarea.php  # 权限点
├── sidebar.php / sidebar_clientarea.php
├── upgrade.php                     # 版本升级脚本（当前为空实现）
├── controller/
│   ├── IndexController.php         # 后台：仪表盘、配置
│   ├── KeyController.php           # 后台：Key 检索 / 同步 / 封禁
│   ├── ChannelController.php       # 后台：渠道状态、模型列表（只读）
│   ├── RedeemController.php        # 后台：卡密批次
│   ├── PushLogController.php       # 后台：推送队列、手动重投
│   └── clientarea/                 # 会员中心（Index / Key / Model / Redeem）
├── model/
│   ├── LlmApi.php                  # 与网关通信的全部客户端（签名、请求、幂等投递）
│   ├── KeyModel.php / UsageModel.php / PushLogModel.php / TxnModel.php / RedeemModel.php
└── logic/
    ├── LlmGatewayLogic.php         # 配置读取 + 套餐映射
    └── CallbackGuard.php           # 免登录回调的 HMAC 自校验
```

### 五张业务表

表名形如 `idcsmart_addon_idcsmart_llm_gateway_{suffix}`：

`key`（Key 本地镜像）、`usage`（用量日聚合，键为 `client_id + host_id + key_id + day`）、
`pushlog`（失败重投队列）、`txn`（额度流水镜像）、`redeem`（卡密批次）。

## 3. 钱为什么不过网关

| 场景 | 谁收钱 | 谁记账 | 网关做什么 |
| --- | --- | --- | --- |
| 买额度（充值） | V10 | V10 订单 + 网关额度账本 | 收到"加额度"指令，幂等入账 |
| 买套餐（订阅） | V10 | V10 订单 + 网关订阅 | 收到"开订阅"指令，绑 Key |
| 买卡密（兑换码） | V10 | V10 卡密表 | 收到"加额度"指令，幂等入账 |
| 退款 | V10 | V10 | 收到"扣额度"指令（`kind=refund`） |

网关侧**没有任何支付代码**，也没有对客户暴露任何收款入口。

### 幂等是怎么保证的

每一次"改变账本"的调用都必须带 `ref_type` + `ref_id` 作为**幂等凭证**。
网关对 `(site_id, ref_type, ref_id)` 建唯一索引：同一凭证重放不会重复入账，
而是返回第一次的结果。

- 加额度：`ref_type=v10_order` / `ref_id=订单号`
- 开订阅 / 续订：`ref_type=v10_host_open` 或 `v10_host_renew`，
  `ref_id` 优先用 `order_id`，其次用 `RENEW-{host_id}-{due_time}`
- **绝不能用 `time()` 之类的会漂移的值做 `ref_id`**——每次重试都会生成新凭证，
  幂等就失效了，客户会白拿时间。插件里已有对应防守，改动时别退回旧写法。

## 4. 安装前准备

- V10 已正常运行，后台可登录
- 网关已部署并可访问，`/internal/v10/health` 返回正常
- 网关侧已给本 V10 站点建好站点凭据（`app_id` / `app_secret`），
  即环境变量 `V10_BOOTSTRAP_APP_ID` / `V10_BOOTSTRAP_APP_SECRET` 所对应的那一组
- 已在网关侧建好订阅套餐，记下 `subscription_plans.id`（见第 7 节）

先自测一次连通性（把地址和密钥换成自己的）：

```bash
curl -s https://你的网关地址/internal/v10/health
```

## 5. 安装步骤

1. 上传两个目录（`czapi_epay` 已移除，钱不过网关）：
   ```
   idcsmart_llm_gateway/  →  站点根/public/plugins/addon/idcsmart_llm_gateway/
   llm_gateway/           →  站点根/public/plugins/server/llm_gateway/
   ```
   > 目录名不能改：V10 按「目录名 = 标识，主类 = 目录名大驼峰，
   > namespace = 上层\目录名」加载。`llm_gateway/` 对应
   > `namespace server\llm_gateway` 与主类 `LlmGatewayModule`，改名会导致模块加载失败。
2. V10 后台 →「插件管理」→ 安装 `LLM 网关`
3. 在同一页面点「模块管理」→ 安装 `LLM 网关模块`
4. 打开插件配置页，填入 `gateway_base_url` / `app_id` / `app_secret`（第 6 节）
5. 点「保存」，再回仪表盘点「测试连接」，确认返回正常
6. 配置商品与套餐映射（第 7 节）

## 6. 配置说明

| 配置键 | 必填 | 说明 |
| --- | --- | --- |
| `gateway_base_url` | **是** | 网关根地址，如 `https://api.example.com`。不要带 `/internal/v10` |
| `app_id` | **是** | 网关分配的站点标识 |
| `app_secret` | **是** | 网关分配的站点密钥（AES-256-GCM 加密存放于网关侧） |
| `app_secret_prev` | 否 | 上一个密钥。轮换期间填这里，让旧密钥在过渡期仍可用 |
| `plan_map` | 否 | 商品 ID → 网关套餐 ID 的映射，见第 7 节 |
| `default_billing_mode` | 否 | 新 Key 的默认计费模式 |
| `max_keys_per_host` | 否 | 每个产品实例允许的 Key 上限 |
| `timeout_ms` | 否 | 请求超时（毫秒），默认 10000 |
| `callback_secret` | 否 | 网关 → V10 免登录回调的自校验密钥。当前契约**没有反向回调**，正常部署应留空 |
| `keep_data_on_uninstall` | 否 | 卸载时是否保留业务表 |

### 关于 `app_secret_prev`

网关侧支持 `rotate` 接口：轮换后**旧密钥立即失效**。若你的运维习惯是
「先改 V10 再改网关」，过渡期会全部 401。正确顺序是：

1. 在插件里把当前密钥挪到 `app_secret_prev`，新密钥填进 `app_secret`（先别保存）
2. 在网关侧执行轮换
3. 立刻回到插件保存

或者更稳妥：接受短暂的推送失败——失败了会进重投队列，五分钟任务会自动补上。

## 7. 商品与套餐映射

`plan_map` 是 JSON，**值是网关的 `subscription_plans.id`（数字），不是字符串编码**：

```json
{
  "12": 1,
  "13": 2,
  "14": 3
}
```

- 键：V10 里的**商品 ID**（数字字符串）
- 值：网关**套餐 ID**（可用整数）

套餐 ID 可以在网关后台「套餐」页查到，或调 `/api/v10/admin/site-options`。

**没配映射的后果**：模块开通时会报「商品未配置套餐」，订单会卡在开通中。
新增商品一定要记得回来补这一行。

## 8. 日常运维

### 自动任务

| Hook | 频率 | 作用 |
| --- | --- | --- |
| `order_paid` | 事件 | 订单支付成功 → 充值类订单加额度；带 `host_id` 的产品订单会**跳过**，交给模块处理 |
| `after_host_delete` | 事件 | 产品删除兜底：终止订阅并吊销 Key（与模块的 `terminateAccount` 幂等重叠，重复调用无副作用） |
| `five_minute_cron` | 5 分钟 | 失败推送重投。让「我付了钱没到账」最多等 5 分钟 |
| `daily_cron` | 每日 | 失败重投 + 用量快照 |

任务输出写在计划任务日志里，搜 `[LLM网关]` 即可看到每次补投了多少条。

### 后台常用操作

- **仪表盘**：网关连通性、站点状态
- **Key 管理**：按客户/产品检索，可封禁或解封；改动会同步到网关
- **渠道状态 / 模型列表**：**只读**。渠道增删改请到网关后台操作
- **卡密批次**：在 V10 这边生成与停用
- **推送队列**：查看失败记录，可单条或全量重投

## 9. 失败重投队列

任何一次 `POST /internal/v10/*` 失败都会落一行 `pushlog`：

- 记录原样保存请求体、`ref_type`、`ref_id`
- `next_retry_at` 按退避时间排下一次尝试
- 重投时**请求体一字不改**，`ref_id` 不变 → 网关侧幂等索引保证不会重复入账
- 重投成功后再调用一次是安全的，这正是不允许用 `time()` 做 `ref_id` 的原因

排查时先看这里：如果队列里堆着同一个 `ref_id` 反复失败，说明是网络或配置问题；
如果堆着每次 `ref_id` 都不同的记录，说明调用方在生成会漂移的凭证——那是 bug。

## 10. 故障排查

| 现象 | 先查什么 |
| --- | --- |
| 保存配置后「测试连接」失败 | `gateway_base_url` 是否多了 `/internal/v10` 后缀；服务器出网是否通 |
| 所有请求 401 `CODE_SIGN_INVALID` | `app_id` / `app_secret` 是否与网关一致；是否刚轮换过密钥 |
| 401 `CODE_TIMESTAMP_WRONG` | 服务器时间偏差超过 5 分钟，校时（NTP） |
| 401 `CODE_NONCE_REPLAY` | 同一 nonce 被重放。检查是否有代理重复发送，或时钟回拨 |
| 403 `CODE_SITE_DISABLED` | 该站点在网关侧被停用 |
| 400 `CODE_MISSING_REF` | 调用没带 `ref_type` / `ref_id` |
| 409 `CODE_REF_CONFLICT` | 同一凭证被用于**不同**的请求内容，多半是 `ref_id` 拼错或复用了 |
| 客户付了钱额度没到 | 看推送队列；看网关 `/internal/v10/accounts/quota` 实际余额 |
| 客户进控制台白屏 | SSO 票据过期（60 秒一次性），刷新页面重新申请 |
| 商品订单卡在开通中 | `plan_map` 里有没有这个商品 ID |

日志在 `runtime/log/` 下当天文件，搜 `[idcsmart_llm_gateway]`。

## 11. 待实例验证清单

以下事项在开发环境无法完全确认，**上线前必须在目标实例上逐条核对**：

1. `order_paid` 事件名。开发文档未逐页核实，若实例上事件名不同，
   把 `hooks.php` 里的 `'order_paid'` 换成实例里的真实事件名即可（回调体不用改）。
2. 若 `order_paid` 从未触发，充值时不会自动加额度。兜底：在插件后台
   「推送队列」页手动补投，或改用 `before_order_pay` + 订单状态二次校验方案。
3. `five_minute_cron` / `daily_cron` 是否已在实例上注册并按时执行。
4. 插件在各主题（默认 / TDesign 后台）下的样式表现。
5. 卡密批次生成后，兑换链路是否全程走 V10。

## 12. 已知限制

- **渠道管理不在 V10 后台。** 网关的渠道增删改需要管理员 JWT，与
  `/internal/v10` 的 HMAC 体系不同源。V10 后台只做只读的状态核验。
- **推送队列是"至多一次有效"语义。** 网关侧幂等保证不会重复入账，
  但如果网关处理成功而响应丢失，V10 会一直以为失败并重投——
  重投是安全的，只是会产生多一条"已幂等忽略"的记录。
- **多副本网关部署时 nonce 去重是进程内的。** 若网关跑多副本且前面没有
  按站点做粘性路由，极端情况下同一 nonce 可能在两个副本各通过一次。
  当前建议网关单副本或用粘性会话。
- **卡密由 V10 生成、V10 兑换。** 网关的 `/api/user/topup` 对 V10 客户是关闭的，
  别试图在网关那边发卡密给客户。
- **卸载插件默认保留数据。** 需要彻底清理时把 `keep_data_on_uninstall` 关掉，
  或在升级前自行备份。

---

相关文档：[接口契约](../docs/internal-v10-contract.md) ·
[部署与验收](../docs/deployment.md) · [项目总览](../README.md)
