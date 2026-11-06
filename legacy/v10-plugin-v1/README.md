# CZAPI × 魔方业务系统 V10 集成包

本目录是「CZAPI（LLM 网关）」与「魔方业务系统 V10（idcsmart）」的**全部 PHP 交付物**，共 3 个可独立安装的组件 + 本文档。

版本：**1.0.0**　适用：魔方业务系统 V10（开发文档基线 V10.5.6）+ PHP 8.0 及以上（已在 PHP 8.3 通过语法检查）

---

## 目录

1. [交付物速览](#1-交付物速览)
2. [安装前准备](#2-安装前准备)
3. [安装步骤](#3-安装步骤)
4. [配置说明](#4-配置说明)
5. [商品与套餐映射](#5-商品与套餐映射)
6. [支付网关配置](#6-支付网关配置)
7. [日常运维](#7-日常运维)
8. [升级](#8-升级)
9. [回滚](#9-回滚)
10. [故障排查](#10-故障排查)
11. [⚠️ 待实例验证清单](#11-️-待实例验证清单)
12. [已知限制](#12-已知限制)

---

## 1. 交付物速览

| 组件 | 源码目录 | 部署到 | 作用 |
| --- | --- | --- | --- |
| 插件 | `idcsmart_llm_gateway/` | `public/plugins/addon/idcsmart_llm_gateway/` | 用户自助面（Key / 用量 / 卡密）+ 后台运营面 + 与网关通信的全部客户端代码 |
| 模块 | `llm_gateway_module/` | `public/plugins/server/llm_gateway_module/` | 把 LLM 套餐做成 V10 可售卖商品：开通/暂停/恢复/删除/续费/按量取数 |
| 支付网关 | `czapi_epay/` | `public/plugins/gateway/czapi_epay/` | 易支付收款通道，跑在 V10 标准支付流程里 |

### 三者职责边界（**不要混用**）

```
V10 订单支付成功
   ├─ 充值/余额类订单 → 插件 hook → POST /internal/v1/credit → 网关加余额
   └─ 套餐类订单     → 模块 createAccount → POST /internal/v1/subscriptions → 网关建订阅
收款/开票/退款 —— 全部在 V10 内部，网关（Go）侧没有任何支付代码。
```

**关键设计约束**：插件与模块**共用同一份网关配置**（插件 `config.php`）。模块通过 `LlmGatewayLogic::config()` 读取，不需要在模块里再填一遍 `app_id` / `app_secret`。因此**插件必须先装、先配**。

### 插件目录结构

```
idcsmart_llm_gateway/
├── IdcsmartLlmGateway.php          # 主类（install/uninstall/upgrade + cron hook 登记）
├── config.php                      # 后台插件配置表单（系统自动渲染）
├── route.php                       # 三组路由（前台免登录回调 / 前台登录 / 后台）
├── hooks.php                       # Hook 注册（订单支付、产品删除、每日/五分钟任务）
├── auth.php                        # 后台权限点
├── auth_clientarea.php             # 前台权限点
├── sidebar.php                     # 后台导航
├── sidebar_clientarea.php          # 前台导航
├── upgrade.php                     # 版本升级脚本
├── controller/
│   ├── IndexController.php         # 后台：仪表盘、配置
│   ├── KeyController.php           # 后台：Key 检索/同步/封禁
│   ├── ChannelController.php       # 后台：渠道状态、模型目录
│   ├── RedeemController.php        # 后台：卡密批次、流水
│   ├── PushLogController.php       # 后台：推送队列
│   └── clientarea/
│       ├── IndexController.php     # 前台：概览、SSO 票据
│       ├── KeyController.php       # 前台：Key 增删改查、额度耗尽回调
│       ├── ModelController.php     # 前台：模型列表、用量
│       └── RedeemController.php    # 前台：卡密兑换
├── model/                          # LlmApi（网关客户端）+ 5 张表的 ORM
├── logic/                          # 配置读取、回调验签、审计
├── validate/                       # Key / 卡密验证器
├── lang/                           # PHP 语言包（zh-cn / zh-hk / en-us）
└── template/
    ├── admin/                      # 后台页面（Vue2 + TDesign）
    └── clientarea/                 # 前台页面（iframe 壳，5 套主题各一份）
```

### 数据表

插件（5 张，前缀 `idcsmart_addon_idcsmart_llm_gateway_`）：

| 表 | 用途 |
| --- | --- |
| `_key` | 网关 Key 的本地镜像（检索/封禁/审计用，**不是事实来源**） |
| `_redeem` | 卡密（只存 `sha256` + 前 8 位前缀，**明文只在生成时出现一次**） |
| `_txn` | 入账单据（本地幂等第一道闸门） |
| `_pushlog` | 推送到网关的可靠队列（指数退避重试） |
| `_usage` | 用量日聚合快照（展示用缓存） |

模块（1 张，前缀 `idcsmart_module_llm_gateway_`）：

| 表 | 用途 |
| --- | --- |
| `_host_subscription` | V10 产品（host）↔ 网关订阅（subscription）映射 |

支付网关：**不建表**（所有订单落在 V10 既有表里）。

---

## 2. 安装前准备

### 2.1 环境要求

- 魔方业务系统 V10，已能正常登录后台
- PHP ≥ 8.0（本项目在 PHP 8.3 上做过 `php -l` 全量语法检查）
- 已开启 `curl` 扩展（插件调网关用）
- 站点已配置**绝对地址**（`global.website_url` 可正确解析到 `https://你的域名`）——易支付回调地址依赖它

### 2.2 网关侧准备

在 CZAPI 网关后台创建一对 V10 专用凭据，记下：

| 项 | 用途 |
| --- | --- |
| `gateway_base_url` | 网关地址，如 `https://api.example.com`（不带结尾斜杠） |
| `app_id` | 应用标识 |
| `app_secret` | 应用密钥（签名用，仅服务端持有） |
| `callback_secret` | 网关回调 V10 时用的密钥（**可选**，见 4.2） |

同时在网关侧确认：
- 已开通 `/internal/v1` 的访问权限
- 已准备好要售卖的套餐（`plan_code`，如 `basic` / `pro` / `max`）

### 2.3 备份

**动手前先备份数据库与站点**：

```bash
# 数据库（把 -u/-p 换成实际账号）
mysqldump -u root -p your_db > backup_$(date +%F).sql

# 站点
tar czf site_backup_$(date +%F).tar.gz /path/to/v10
```

---

## 3. 安装步骤

> 顺序不能颠倒：**先插件（并提供配置）→ 再模块 → 最后支付网关**。
> 模块依赖插件里的网关客户端与配置。

### 3.1 安装插件

1. 上传目录：
   ```
   idcsmart_llm_gateway/  →  站点根/public/plugins/addon/idcsmart_llm_gateway/
   ```
2. 后台进入「应用 → 插件管理」，找到「LLM 网关」，点**安装**。
   - 安装时会自动创建 5 张表，并登记 2 个定时任务 Hook（`daily_cron`、`five_minute_cron`）。
3. 点**配置**，按 [4. 配置说明](#4-配置说明) 填写。

**验证**：后台侧栏出现「LLM 网关」分组，5 个页面均可打开；前台会员中心侧栏出现「LLM 网关」入口。

### 3.2 安装模块

1. 上传目录：
   ```
   llm_gateway_module/  →  站点根/public/plugins/server/llm_gateway_module/
   ```
2. 后台进入「产品 → 接口管理（服务器）」，刷新后出现「LLM 网关套餐」。
3. 点**配置**（如有），填入与插件相同的网关地址与凭据 —— 但通常**留空即可**，模块会自动复用插件配置。
4. **测试连通**：点接口列表里的状态探测按钮，应显示「连接成功」。

**验证**：接口状态灯为绿；若为红，先去看 [10. 故障排查](#10-故障排查)。

### 3.3 安装支付网关

1. 上传目录：
   ```
   czapi_epay/  →  站点根/public/plugins/gateway/czapi_epay/
   ```
2. 后台进入「财务 → 支付接口」，刷新后出现「易支付」。
3. 点**安装**，再点**配置**，填写 pid / key / api_url（见 [6. 支付网关配置](#6-支付网关配置)）。

**验证**：前台下单 → 选择「易支付」→ 能跳到易支付收银台 → 付款 → 订单自动变为已支付。

---

## 4. 配置说明

后台「应用 → 插件管理 → LLM 网关 → 配置」：

### 4.1 必填项

| 配置键 | 类型 | 说明 |
| --- | --- | --- |
| `gateway_base_url` | 文本 | 网关根地址，如 `https://api.example.com`。**不要带结尾斜杠** |
| `app_id` | 文本 | 网关分配的应用 ID |
| `app_secret` | 密码 | 应用密钥。保存后不会完整回显 |
| `callback_secret` | 密码 | 网关回调 V10 的验签密钥。留空 = **关闭回调接口**（推荐，见 4.2） |

### 4.2 关于 `callback_secret`（重要）

`console/v1/api/llm/callback/quota-exhausted` 这组路由是「**预留但默认拒绝**」的：

- 不填 `callback_secret` → 该接口一律返回 401，等于关闭
- 填了 → 网关可以用 HMAC 签名调用，用于额度耗尽时主动通知 V10

本项目**不依赖**它工作。当前 `/internal/v1` 契约（v1）**没有反向回调**，所以正常部署下应当**留空**。只有将来网关侧确实实现了主动通知才需要填写。

### 4.3 可选项

| 配置键 | 默认 | 说明 |
| --- | --- | --- |
| `app_secret_prev` | 空 | 上一个密钥。用于密钥轮换的过渡期，两个密钥都能验签 |
| `plan_map` | `{}` | 商品 ID → `plan_code` 的映射（JSON）。见 [5. 商品与套餐映射](#5-商品与套餐映射) |
| `default_billing_mode` | `hybrid` | 商品未配置 `billing_mode` 时的默认计费模式：`subscription` / `balance` / `hybrid` |
| `max_keys_per_host` | `5` | 每个产品最多能创建多少把 Key（防滥用） |
| `timeout_ms` | `5000` | 调网关的超时（毫秒）。跨地域部署建议 8000–10000 |
| `keep_data_on_uninstall` | `1`（保留） | 改 `0` 后卸载插件会**连表一起删**。见 [9. 回滚](#9-回滚) |

---

## 5. 商品与套餐映射

模块需要知道「这个 V10 商品对应网关的哪个套餐」。有两种配法，**任选其一**：

### 方式 A：商品自定义字段（推荐）

在 V10 后台「产品 → 商品 → 编辑 → 自定义字段」里给该商品加：

| 字段名 | 必填 | 取值 | 说明 |
| --- | --- | --- | --- |
| `plan_code` | **是** | `basic` / `pro` / `max` | 网关里的套餐编码 |
| `quota_tokens` | 否 | 数字 | 覆盖套餐默认额度（token 数） |
| `billing_mode` | 否 | `subscription` / `balance` / `hybrid` | 计费模式 |
| `model_group` | 否 | `standard` / `premium` / `all` | 模型范围 |
| `rpm` | 否 | 数字 | 每分钟请求上限 |
| `tpm` | 否 | 数字 | 每分钟 token 上限 |
| `concurrency` | 否 | 数字 | 并发上限 |

### 方式 B：插件配置里的 `plan_map`

商品没有自定义字段能力时，用插件配置的 `plan_map` JSON 兜底：

```json
{
  "12": "basic",
  "13": "pro",
  "14": "max"
}
```

键是**商品 ID**（数字），值是 `plan_code`。

### 校验规则

- 两者都没配 → `createAccount` 返回 `status=400`，订单停在「开通失败」，`msg` 会明确写出商品 ID 和需要配什么
- `billing_mode` 取值非法 → 同样报 400
- **不会静默成功**：宁可让订单失败，也不让用户付了钱却没有套餐

---

## 6. 支付网关配置

后台「财务 → 支付接口 → 易支付 → 配置」：

| 配置键 | 说明 |
| --- | --- |
| `pid` | 易支付商户 ID（纯数字） |
| `key` | 易支付商户密钥 |
| `api_url` | 易支付站点根地址，如 `https://pay.example.com`（**不带结尾斜杠**，插件自动拼 `/submit.php`） |
| `default_type` | 默认支付方式：`alipay` / `wxpay` / `qqpay` |

### 签名规则

参数剔除空值、`sign`、`sign_type` 后按**键名 ASCII 升序**拼成 `k=v&k=v`，末尾**直接**拼 `key`（没有 `&key=`），取 MD5 小写。

### 回调地址

插件会自动生成，无需手工配置：

```
异步通知：https://你的域名/gateway/czapi_epay/index/notifyHandle
同步跳转：https://你的域名/gateway/czapi_epay/index/returnHandle
```

- **异步通知**返回裸文本 `success`（易支付只认这个）
- **同步跳转**只验签+展示结果，**不入账**（防止用户刷新页面重复入账）
- 入账统一调 V10 的 `order_pay_handle()`

### 丢回调兜底

易支付偶发丢回调。`gateway/czapi_epay/index/cronHandle` 会主动查单补入账，建议挂到计划任务每 5–10 分钟跑一次（用 curl 或浏览器定时访问）。

---

## 7. 日常运维

### 7.1 后台页面

| 页面 | 路径 | 用途 |
| --- | --- | --- |
| 仪表盘 | `{DIR_ADMIN}/plugin/idcsmart_llm_gateway/index.html` | 网关状态、Key 统计、卸载策略 |
| Key 管理 | `.../key.html` | 检索/同步/封禁用户 Key |
| 渠道与模型 | `.../channel.html` | 网关健康状态 + 模型目录（只读） |
| 卡密 | `.../redeem.html` | 生成批次、导出 CSV、启停批次、查流水 |
| 推送队列 | `.../pushlog.html` | **客户投诉「充了没到账」时的自助修复入口** |

### 7.2 卡密

生成批次时**明文只在弹窗里出现这一次**，库里只有 `sha256` 与前缀。弹窗提供「复制」与「导出 CSV」——**必须当场保存**，关掉就再也拿不回来了。

卡密格式：20 位，字符集 `23456789ABCDEFGHJKLMNPQRSTUVWXYZ`（32 字符，去掉了 `0`/`1`/`I`/`O` 等易混淆字符）。

### 7.3 推送队列

入账流程是「先写本地队列 → 再调网关」：

```
写 _pushlog(pending) → 调 /internal/v1/credit
  ├─ 2xx            → done
  ├─ 4xx(幂等冲突)  → done
  └─ 5xx/超时        → failed，指数退避：1,2,4,8,16,30,30… 分钟，最多 12 次
```

定时任务 `five_minute_cron` 每 5 分钟自动补投 50 条，`daily_cron` 每天补投 200 条。

后台「推送队列」页可以查看状态、单条重投、批量重投。**重投是安全的**——网关用 `(ref_type, ref_id)` 唯一索引保证幂等，不会重复加额。

### 7.4 定时任务

插件安装时会自动往 `idcsmart_plugin_hook` 表登记两条记录。若你的实例没自动登记（见 [10. 故障排查](#10-故障排查)），手工执行：

```sql
INSERT INTO `idcsmart_plugin_hook`(`name`,`status`,`plugin`,`module`,`order`)
VALUES ('daily_cron',1,'IdcsmartLlmGateway','addon',0);

INSERT INTO `idcsmart_plugin_hook`(`name`,`status`,`plugin`,`module`,`order`)
VALUES ('five_minute_cron',1,'IdcsmartLlmGateway','addon',0);
```

---

## 8. 升级

1. **备份**数据库与站点（同 2.3）
2. 覆盖上传新版本文件（保持目录名不变）
3. 后台「插件管理」里点该插件的**升级**
   - `upgrade()` 会重新执行全部 `CREATE TABLE IF NOT EXISTS`，缺表自动补建
   - 幂等，可重复执行
4. 若新版本有配置项变更，回到「配置」页确认一遍
5. 清一次站点缓存（后台「系统 → 缓存管理」）

**升级不会丢数据**：所有建表语句都是 `IF NOT EXISTS`，没有任何 `DROP`。

---

## 9. 回滚

### 9.1 只想停用（不删数据）

后台「插件管理」→ 卸载。

`keep_data_on_uninstall` 默认为 `1`（保留），所以：**5 张表的数据全部保留**，只清理配置与导航。

重装后数据还在，可以接着用。

### 9.2 彻底卸载（连数据一起删）

**这一步不可恢复。** 执行前务必确认备份存在。

1. 后台「插件管理 → LLM 网关 → 配置」，把 `keep_data_on_uninstall` 改成 **`0`（删除数据）**，保存
2. 再点**卸载**

或者手工删表：

```sql
DROP TABLE IF EXISTS `idcsmart_addon_idcsmart_llm_gateway_key`;
DROP TABLE IF EXISTS `idcsmart_addon_idcsmart_llm_gateway_redeem`;
DROP TABLE IF EXISTS `idcsmart_addon_idcsmart_llm_gateway_txn`;
DROP TABLE IF EXISTS `idcsmart_addon_idcsmart_llm_gateway_pushlog`;
DROP TABLE IF EXISTS `idcsmart_addon_idcsmart_llm_gateway_usage`;
DROP TABLE IF EXISTS `idcsmart_module_llm_gateway_host_subscription`;
```

### 9.3 回滚到上一个版本

1. 恢复代码：用备份的目录覆盖回去
2. 恢复数据库：`mysql -u root -p your_db < backup_YYYY-MM-DD.sql`
3. 后台清缓存

### 9.4 回滚会不会影响网关上已发放的额度？

**不会。** 网关是额度的**唯一事实来源**。V10 侧删表只丢失「本地镜像与流水」，用户已经拿到的额度、Key 都不受影响。但：

- 卡密明文已不可恢复（只存了 hash）
- 本地推送队列会丢，若当时有 `pending` 记录，需要人工核对网关侧的加额记录

---

## 10. 故障排查

### 10.1 后台页面 404 / 空白

| 检查 | 做法 |
| --- | --- |
| 目录名对不对 | 必须是 `idcsmart_llm_gateway`（全小写下划线） |
| 主类文件名 | 必须是 `IdcsmartLlmGateway.php`（目录名大驼峰） |
| 命名空间 | 首行必须是 `namespace addon\idcsmart_llm_gateway;` |
| PHP 版本 | `php -v` ≥ 8.0 |
| 文件权限 | `public/plugins/addon/idcsmart_llm_gateway/` 需对 PHP 进程可读 |

### 10.2 「网关不可达」

1. 服务器上直接测：`curl -i https://你的网关地址/internal/v1/health`
2. 检查 `gateway_base_url` 是否带了多余的路径或结尾斜杠
3. 检查 `app_id` / `app_secret` 是否正确（密钥错会返回 `40100` 签名错误）
4. 检查服务器时间：签名有时间窗（±300 秒），时间不同步会返回 `40101`
5. 跨地域部署把 `timeout_ms` 调到 8000–10000

### 10.3 订单卡在「开通失败」

看后台的**开通失败原因**（模块返回的 `msg`），常见三种：

| `msg` 内容 | 处理 |
| --- | --- |
| `商品未配置套餐（plan_code）…` | 按 [第 5 节](#5-商品与套餐映射) 给商品加 `plan_code` 字段，或配 `plan_map` |
| `创建网关账户失败…` | 网关地址/凭据不对，或网关不可达 —— 回到 10.2 |
| `未找到产品 #N 对应的网关订阅…` | 该产品是升级前就存在的，没有订阅映射。到插件后台 Key 页点一次「同步」，再重试；仍不行则需在网关侧手工建订阅 |

### 10.4 用户充值了但额度没到账

1. 打开后台「推送队列」页
2. 按业务单号（订单号）搜索
3. 找到状态为 `failed` 的记录，看 `last_error`
4. 修复根因（通常是网关不可达）后点**重投**

若队列里**根本没有**这条记录，说明 hook 没触发，见 10.5。

### 10.5 充值 hook 没触发

排查顺序：

1. 后台「插件管理 → LLM 网关」，确认插件处于**已安装**状态
2. 确认 `hooks.php` 已在插件目录里（漏传会导致所有 hook 静默失效）
3. 到 V10 后台的 **Hook 列表**里确认 `order_paid` 存在
   - 若不存在：本项目的 `hooks.php` 里把它换成实例上的真实事件名即可，回调函数体不用改
4. 兜底：即使 hook 完全不可用，也可以按 10.4 的方式人工补投

### 10.6 卡密兑换提示「处理中，稍后自动重试」

这是**设计行为**，不是错误：

卡密已经消费（`redeemed_at` 已写入，防重复兑换），但网关当时不可达。系统已经写好入账单据，`five_minute_cron` 会自动补投，**最多 5 分钟到账**。

不要撤销、不要让用户再试一次 —— 再试会提示「卡密已使用」。

### 10.7 前台「LLM 网关」页面空白

前台页面是 **iframe 壳**：它先调 `GET console/v1/llm/sso-ticket` 拿一次性票据，再把 `sso_url` 赋给 `iframe.src`。空白通常是：

1. 票据接口报错 → 浏览器控制台看 `/console/v1/llm/sso-ticket` 的响应
2. `sso_url` 指向的网关控制台域名不可访问 → 浏览器直接打开那个 URL 试试
3. 浏览器拦截了第三方 iframe → 检查网关控制台的 `X-Frame-Options` / CSP `frame-ancestors` 是否允许你的站点域名

### 10.8 易支付付款后订单没变

1. 确认易支付商户后台的**通知地址**是否配置成功（付款后看易支付那边的回调记录）
2. 站点必须是**可公网访问的 HTTPS 绝对地址**（回调是服务器对服务器）
3. 手工触发一次查单：访问 `https://你的域名/gateway/czapi_epay/index/cronHandle`
4. 看 `runtime/log/` 下当天的日志，搜 `[czapi_epay]`

### 10.9 定时任务不跑

1. 确认 `idcsmart_plugin_hook` 表里有本插件的两条记录（见 7.4）
2. 确认 V10 的系统计划任务本身在跑（后台一般有任务执行记录）
3. 即使定时任务完全不可用，推送队列也能在后台页面手工重投

---

## 11. ⚠️ 待实例验证清单

以下每一项都**无法在当前环境验证**（没有 V10 实例、没有 MySQL、没有网关服务），必须在目标实例上线前逐条确认。代码里对应位置都有 `// ⚠️ 待实例验证:` 注释。

### 11.1 网关回调协议（自定协议，非 V10 规定）

| # | 项 | 现状 | 需要确认 |
| --- | --- | --- | --- |
| 1 | `logic/CallbackGuard::verify()` 与 `LlmGatewayLogic::verifyCallback()` | 自定的 HMAC 协议（`X-CZ-App-Id/Timestamp/Nonce/Signature`） | `docs/internal-api-v1.md` §4 明确 v1 **没有**反向回调，所以这套协议是**本项目的预留**。若将来用：需确认①回调组能读到原始 body（`getContent()`）②自定义 `X-CZ-*` 头能穿过 `ParamFilter` 与 Web 服务器 |

**影响**：不填 `callback_secret` 时该接口一律 401，等于关闭。正常部署应留空。

### 11.2 Hook 事件名

| # | 项 | 现状 | 需要确认 |
| --- | --- | --- | --- |
| 2 | `order_paid` | plan 附录 A 提到；**已核对开发文档「公共业务 Hook/订单、支付与退款」的 21 个条目，未见直接的「订单支付后」条目** | **必须**在实例的后台 Hook 列表确认实际事件名。这是充值入账的**唯一触发点** |
| 3 | `daily_cron` | 已在开发文档「定时任务/002-每日定时任务」页核实 ✅（注意：**不是** `day_cron`） | 参数为 `config`、`output` |
| 4 | `five_minute_cron` | 已在开发文档「定时任务/004-每五分钟定时任务」页核实 ✅，且官方插件 `upgrade()` 里有登记语句佐证 | 需确认 `idcsmart_plugin_hook` 表能正常插入 |
| 5 | `after_host_delete` | 模块开发文档给出的 `add_hook` 官方示例事件 ✅ | — |
| 6 | 模块 `get_host_flow` hook 的返回结构 | 主类 `getHostFlow()` 返回 `total`/`amount`/`cost_micros` 等多种命名 | 按量计费出账时 V10 实际读哪个字段 |

### 11.3 V10 数据表与结构

| # | 项 | 现状 | 需要确认 |
| --- | --- | --- | --- |
| 7 | `LlmApi::onOrderPaid()` 读 `order` 表 | 按 `id`/`client_id`/`amount`/`host_id` 读取 | 实际字段名；以及「充值订单 vs 产品订单」的区分方式（当前靠 `host_id > 0`） |
| 8 | 支付网关读 `tmp_order` 表 | 按 `tmp_order_id`/`total` 取应付金额 | 实际表名与字段；取不到时会**跳过金额校验**而非拒绝 |
| 9 | `idcsmart_plugin_hook` 表的列集合 | 按官方插件 INSERT 语句推断（`name`/`status`/`plugin`/`module`/`order`） | 是否有额外必填列（插入失败已被 try/catch 捕获，不阻断安装） |
| 10 | 模块 `createAccount($param)` 的参数结构 | 按官方文档 `host`/`client`/`product`/`server` 四段处理，并对 `host_id`/`client_id` 做了兼容 | 实际传入的键名与嵌套层级 |

### 11.4 模块相关

| # | 项 | 现状 | 需要确认 |
| --- | --- | --- | --- |
| 11 | 运行期 `CREATE TABLE IF NOT EXISTS` | 模块没有像插件那样的 `install()` 时机，因此在 `ensureSchema()` 里运行期建表 | 实例是否允许运行期 DDL。若禁止，需在 `afterCreateFirstServer()`（若确实会被调用）或维护脚本里预建 |
| 12 | `afterCreateFirstServer()` 是否真会被调用 | 按 plan §9 实现 | 实例上确认；无论如何 `createAccount()` 里也兜底调了 `ensureSchema()` |
| 13 | 模块 `metaData()` 返回的字段集合 | 只返回 `display_name`/`version`/`description` 三个最保守的键 | 实例是否需要更多键（如 `pay_type`、`module_name` 等） |
| 14 | 续费时长字段名 | 兼容 `duration`/`unit`、`custom['duration']`、`host['duration']` 三种 | 实际传入的是哪一个 |

### 11.5 前端与页面

| # | 项 | 现状 | 需要确认 |
| --- | --- | --- | --- |
| 15 | `goConfigPage()` 跳转地址 | 跳到 `../plugin/index.html`（插件列表页） | 没有已核实的「插件配置页」URL，因此退到插件列表让运维自己点「配置」 |
| 16 | 前台主题目录名 | 已按 `pc/{default,mf801,mf_pc_701}`、`mobile/{mfm201,mfm301}` 各出一份 | 目标站点实际使用的主题目录名（plan Q2'）。**5 套都提供，缺哪套都不会回落** |
| 17 | 移动端模板的 Vue 版本假设 | `mfm201`/`mfm301` 按 **Vue3 + Vant** 写（源码实证：`Vue.createApp` + `app.use(vant).mount()`） | 若实例这两个模板其实是 Vue2，页面不会渲染 |

### 11.6 其它

| # | 项 | 现状 | 需要确认 |
| --- | --- | --- | --- |
| 18 | 卡密失败计数用 `Cache` | 每人每日 5 次失败上限，用 `think\facade\Cache` 存计数 | 依赖 `cache.driver` 在多进程间共享（file 或 redis 都行；array 驱动不行） |
| 19 | 卡密 hash 无盐 | `sha256(码)`，按 plan §10.2 | 加盐需要给批次表加 salt 列；20 位 32 字符集的熵已足够抵抗离线爆破 |
| 20 | 易支付查单接口 | `api.php?act=order&pid=&key=&out_trade_no=` | 不同易支付发行版路径/字段可能不同（`status` vs `trade_status`、`money` vs `price`），已做兼容但需实测 |
| 21 | 支付网关读取自身配置的途径 | 依次尝试 `plugin_config()` → `config('config')` → 本地 `config.php` 默认值 | 官方文档只说明 `config.php` 会被渲染成表单，**未写明运行期读取方式**。上线后确认哪条生效，可删掉多余的 |
| 22 | `LlmApi::request()` 读 `verify_response_sign` | 该键**没有**在 `config.php` 里声明（默认 `'0'` = 关闭） | 若网关将来对响应也签名，需要把这个键补进 `config.php` |

---

## 12. 已知限制

### 12.1 有意为之的设计取舍

1. **台账双写但不做双向同步**。网关是额度的唯一事实来源；`_key` / `_usage` 只是本地镜像与展示缓存。本地镜像过期不影响正确性（网关仍会按自己的数据拒绝或放行）。

2. **卡密明文不可恢复**。只存 `sha256` + 前 8 位前缀。运营必须在生成时当场复制/导出 CSV。这是安全上的必然代价。

3. **卡密兑换遇到网关不可达时不回滚**。卡密标记为已消费、单据保持 `pending`、队列自动重试。回滚会让「重复兑换」成为可能，风险更大。用户看到的是「处理中，稍后自动重试」，最多 5 分钟。

4. **充值/套餐两条入账路径严格分离**。`host_id > 0` 的订单由模块处理，`onOrderPaid()` 会主动跳过。这样「套餐额度」和「余额」不会被加两次。

5. **前台用 iframe 而不是原生页面**。V10 前台是 Vue2 + Element UI，网关控制台是 Vue3，两套 VDOM 无法共存。iframe + 一次性 SSO 票据是唯一稳的做法，也不随主题升级而失效。

6. **渠道增删改不在 V10 后台**。网关的 `/admin/v1/channels` 需要管理员 JWT，与 `/internal/v1` 的 HMAC 体系不同源。V10 后台只做**只读**的状态核对与模型目录查看。

### 12.2 当前不做的

- **按量计费出账**（`get_host_flow`）：代码已就绪，但 plan 把它排在 P4，且返回结构待实例验证
- **应用商店上架**：不需要原生插件页（方式 B）
- **多站点/多租户隔离**：当前一套插件对应一个 V10 站点
- **退款自动冲减网关额度**：退款走 V10 原有流程，网关额度需人工调整

### 12.3 安全相关

- `app_secret` / `callback_secret` 用 `type=password`，保存后前端只见尾 4 位
- 所有网关调用都在服务端，前端拿不到密钥
- SSO 票据一次有效、60 秒过期、绑定 `client_id`
- 卡密兑换有每人每日 5 次失败上限（防爆破）
- 易支付回调必须验签 + 比对商户号 + 比对金额，三重校验
- 同步跳转**不入账**，只有异步通知与主动查单才入账

---

## 附：校验记录

本交付物在构建时执行了以下校验（结果：全部通过）：

| 校验项 | 方法 | 结果 |
| --- | --- | --- |
| PHP 语法 | `php -l` 逐个检查 | **36 个 PHP 文件全部通过** |
| 编码 | 读文件头 3 字节判 BOM + 严格 UTF-8 解码 | **68 个文件全部无 BOM、全部合法 UTF-8** |
| 路由 → 控制器 | 脚本解析 `route.php`，逐条验证类文件与方法存在 | **30 条路由全部通过** |
| `extends` 基类 | 脚本解析所有 `extends`，比对已核实基类白名单（自动解析 `use` 别名） | **17 处全部通过** |
| 类引用存在性 | 脚本解析所有 `use addon\idcsmart_llm_gateway\...`，验证对应文件存在 | **34 处全部通过** |
| 前端语言键 | 脚本比对模板中用到的 `lang.xxx` 与 `template/*/lang/*.js` 定义 | **全部命中** |
| PHP 语言键 | 脚本比对代码中 `lang_plugins('xxx')` 与 `lang/*.php` 定义 | **27 处全部命中** |

已核实的基类白名单：

- `app\common\lib\Plugin` — 插件主类
- `app\event\controller\PluginBaseController` — 前台控制器
- `app\event\controller\PluginAdminBaseController` — 后台控制器
- `think\Model` / `think\Validate` — ThinkPHP ORM / 验证器
