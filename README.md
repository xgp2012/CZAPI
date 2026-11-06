# CZAPI

面向 **魔方业务系统 V10**（idcsmart 主机面板）的 LLM 网关。

V10 卖出的是"额度"和"订阅"；这个网关负责把这些东西变成真正能调用的
OpenAI 兼容接口——鉴权、转发、计量、额度账本。

> **网关不经手任何一分钱。**
> 商品、订单、支付、退款、卡密全部留在 V10。网关只接收一条指令：
> 「这个客户 +N 微元额度」，并用 `(ref_type, ref_id)` 唯一索引保证
> V10 重推时不会重复加钱。

---

## 为什么是二开而不是自建

本项目**推翻**了早期自建 Go 网关的方案，改为在 [new-api](https://github.com/QuantumNous/new-api)
（`v1.0.0-rc.48`）上二开。

理由很实际：new-api 已经有 40+ 上游渠道适配、渠道故障转移、预扣—结算、
令牌管理、订阅套餐、多种登录方式。这些都不产生业务差异化，但每一个都要花
几个月才能做到"不出错"。真正属于本项目的那一层——**与 V10 的对接边界**——
反而是小而清晰的，正好适合单独写。

因此代码分成两块，职责不重叠：

| 目录 | 内容 | 改了什么 |
| --- | --- | --- |
| `gateway/` | new-api 二开 | 新增 `internalv10/` 包，改动 `main.go`、`router/`、`controller/setup.go` |
| `v10-plugin/` | V10 插件 | 全新编写，让 V10 把网关当成一种可售商品 |
| `docs/` | 契约与运维 | `/internal/v10` 契约、部署手册 |
| `legacy/` | 归档 | 早期自建方案，只读参考 |

---

## 边界

```
        ┌──────────────────────────────┐
        │  魔方 V10                     │
        │  商品 · 订单 · 支付 · 卡密     │
        │  客户 · 订阅 · Key 可视化      │
        └───────────┬──────────────────┘
                    │ /internal/v10（HMAC 签名）
                    │ 只传"给谁加多少额度"、"建个 Key"
        ┌───────────┴──────────────────┐
        │  网关（new-api 二开）          │
        │  鉴权 · 转发 · 计量 · 额度账本  │
        │  不管钱，只管账                │
        └───────────┬──────────────────┘
                    │ OpenAI 兼容
              ┌─────┴─────┐
              │  上游模型  │
              └───────────┘
```

由此推出三条硬约束，全部在启动时强制执行，不靠运维记得点后台：

1. **V10 是唯一入口**：网关关闭自助注册、密码注册、邮箱验证、自助充值。
2. **客户没有网关密码**：账号由 V10 调用 `accounts/sync` 建立，
   进控制台一律走 60 秒一次性的 SSO 票据。
3. **运维仍能进后台**：密码登录只对运维账号有效（V10 客户的密码是
   无法命中的占位值），不会被自己的锁面挡在门外。

---

## 快速开始

最短路径是先跑通再上生产。完整步骤（三种部署形态、MySQL/PG 配置、
反向代理、V10 插件安装）见 **[部署与运维手册](docs/deployment.md)**，
接口契约见 **[`docs/internal-v10-contract.md`](docs/internal-v10-contract.md)**。

```bash
# 1. 构建（两个前端都必须先构建，否则 Go 编不过）
cd gateway/web && bun install --frozen-lockfile \
  && DISABLE_ESLINT_PLUGIN=true VITE_REACT_APP_VERSION=$(cat ../VERSION) bun run build
cd ../admin-ui && bun install && bun run build && cd ..
CGO_ENABLED=0 go build -o czapi-gw .

# 2. 启动（不设 SQL_DSN 就用内置 SQLite，零外部依赖）
#    首次启动自动建表、锁定用户面、注册引导站点、创建后台账号
PORT=10000 SESSION_SECRET="$(openssl rand -hex 32)" \
V10_BOOTSTRAP_APP_ID=v10test \
V10_BOOTSTRAP_APP_SECRET="$(openssl rand -hex 32)" \
V10_CONSOLE_BASE_URL=http://127.0.0.1:10000 \
./czapi-gw

# 3. 首次打开 http://127.0.0.1:10000/console/ 会进入安装向导
#    数据库检查 → 管理员账号 → 使用模式 → V10 站点（可跳过）→ 确认初始化
#    在「V10 站点」一步填 App ID，初始化时会顺手建好站点并把密钥显示一次，
#    照抄进 V10 插件即可；留空则之后到「V10 运营 → 站点」补建。

# 4. 验收
php scripts/v10-verify-all.php http://127.0.0.1:10000 <app_id> <secret>
```

六个套件共 102 条断言，全绿才算可用（运维流程、插件契约、host 生命周期、
SSO 会话、密钥轮换、用户面封锁）。

---

## 关键设计

**加额度靠幂等键，不靠人工。** 修账的正确方式是重放请求，不是写 SQL。
`v10_refs` 上 `(site_id, ref_type, ref_id)` 唯一，重复提交只生效一次，
第二次返回 `applied: false` 并回放首次余额。

**金额先乘后除。** 1 元 = 1,000,000 微元；new-api 额度 1 元 = 500,000 quota。
换算必须是 `micros * QuotaPerUnit / 1_000_000`。先除会把零头截断——
2.5 元会被记成 2 元，这是真实的资损。

**吊销与暂停必须分开。** 暂停（欠费）可恢复；吊销（删除产品）是永久的。
早期两者都置 `disabled`，导致"恢复套餐"把客户早就作废的 Key 全复活了。

**续期从到期时间顺延，不从当前时间重算。** 否则客户提前续费反而少拿时间。

**用量聚合在 Go 里做，不下推 SQL。** 为了同时兼容 SQLite / MySQL / PostgreSQL，
避开 `FROM_UNIXTIME`、`DATE_FORMAT` 这类方言函数。

**SSO 票据走 URL fragment。** 不进服务端访问日志；60 秒过期；只能一次；
库里只存 SHA-256 哈希；消费用 `UPDATE ... WHERE used_at = 0` 原子抢占。

---

## 已知限制

诚实列出，不掩饰：

- **数据库验证：SQLite 与 PostgreSQL 已实测，MySQL 未验证。**
  SQLite（内置）与 PostgreSQL 18.6 均跑通全部 5 个套件，含二次启动幂等与
  `v10_refs` 唯一约束。MySQL ≥ 5.7.8 因本机无实例、无 Docker **未验证**——
  代码避开了方言（聚合在 Go 侧做、行锁走 `lockForUpdate`），但 MySQL 的
  唯一冲突错误分支没在真实实例上跑到过。上生产前必须补测。
- **控制台已融合 V10 运营面。** 站点、托管账户、账本、Key、订阅、用量六类运维页面
  已并入 React 控制台的「V10 运营」分组（`/v10/*`），与 new-api 原生页面共用同一套
  登录与侧边栏，后台鉴权走双凭据中间件：new-api 管理员会话与旧 `czapi_admin`
  cookie 都能进。旧的 Vue `/admin/` 仍可访问，但不再是主路径。
- **nonce 去重是进程内的。** 单副本无问题；多副本下同一 nonce 可能各通过一次。
  签名本身仍安全（nonce 只防重放），但严格一次性语义需外置存储。

---

## 许可与署名

本项目基于 [new-api](https://github.com/QuantumNous/new-api) 二次开发，
沿承其 **AGPL-3.0** 许可及 Section 7 附加条款，并保留原项目的署名与链接：

> Frontend design and development by New API contributors.
>
> 原项目：https://github.com/QuantumNous/new-api
>
> Copyright (c) QuantumNous and contributors.

上述署名不只是写在文档里：它已作为独立一行渲染在控制台页脚，
并带可点击的原项目链接（见 `web/src/components/layout/components/footer.tsx`）。
上游原本只有 "Designed and developed by the project contributors."，
不等同于 Section 7(b) 要求的原文，因此本修改版额外补了一行。
**该行不可删除，删除即违反许可。**

依据 AGPLv3 Section 7(c)，本项目相对上游的改动已明确标注：

| 文件 | 改动 |
| --- | --- |
| `internalv10/`（新增） | V10 对接层全部代码 |
| `internalv10/guard.go`（新增） | 禁止 V10 托管账户使用网关自带的付费/兑换/订阅自购入口 |
| `main.go` | 在 `model.InitOptionMap()` 之后调用 `v10.Bootstrap()` |
| `router/main.go`、`router/api-router.go` | 挂载 `/internal/v10`、`/api/v10/*` 与 `/api/v10/admin/*`；给自服务付费路由挂身份守卫 |
| `router/web-router.go` | 用按路径段匹配的 `isAPIPath()` 取代 `HasPrefix("/v1")`，修复 `/v10/login` 被误判为 API 路径 |
| `controller/setup.go` | 初始化后重新施加 V10 用户面约束；新增可选「V10 站点」步骤，建站并一次性回显密钥 |
| `middleware/access_token_routes.go` | 为 `/api/v10/admin/*` 与 `/api/v10/console/*` 声明访问令牌规则（一律 session） |
| `model/v10_cache_bridge.go`（新增） | 导出额度缓存同步函数 |
| `web/src/features/auth/sso/`（新增）、`web/src/routes/v10/login.tsx`（新增） | SSO 票据登录页 |
| `web/src/features/v10/`（新增）、`web/src/routes/_authenticated/v10/`（新增） | V10 运营面并入 React 控制台（概览/站点/账户/账本/Key/订阅/用量） |
| `web/src/features/setup/` | 安装向导新增可选「V10 站点」步骤，初始化后一次性展示密钥 |
| `web/src/components/layout/components/footer.tsx` | 补上 Section 7(b) 要求的**逐字**署名行并附原项目链接 |

完整许可见 [`gateway/LICENSE`](gateway/LICENSE) 与
[`gateway/NOTICE`](gateway/NOTICE)。`gateway/NOTICE` 必须随二进制、
Docker 镜像与前端产物一同分发。

## 目录

```
gateway/                new-api 二开（AGPL-3.0）
  internalv10/          V10 对接层：签名、账户、额度、Key、host、SSO、用量
  admin-ui/             运维后台源码（Vue3 + Element Plus）
  adminui/dist/         运维后台构建产物（已提交，供 //go:embed 使用）
  web/                  客户控制台（new-api 原生，React）
v10-plugin/             V10 插件：addon + 服务端模块
  idcsmart_llm_gateway/   插件主体（配置、路由、控制器、模型）
  llm_gateway/            商品开通/续费/暂停/终止的模块实现
docs/                   /internal/v10 契约 + 部署运维手册
scripts/                端到端验收（插件契约、host 生命周期、SSO、轮换、用户面封锁）
legacy/                 早期自建方案（只读参考，已被取代；见 legacy/README.md）
```

运维面已并入客户控制台：`/console/` 是唯一入口，new-api 原生页面（渠道、模型、
用户、系统设置）与「V10 运营」分组（站点、托管账户、账本、Key、订阅、用量）共用
同一套会话与侧边栏。旧的 Vue `/admin/` 仍保留可访问，但功能已在 React 侧复刻。
