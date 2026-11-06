# CZAPI 控制台（web/）

Vue 3 + TypeScript + Vite 单页应用，同时承载**用户中心**与**管理后台**两块界面。
契约严格对齐 `docs/console-api-v1.md`（冻结文件），字段名、路径、单位一字不改。

- 金额：`*_micros`，1 元 = 1_000_000 微元，前端只做除 1e6 显示
- 时间：一律 unix 秒（int64），在 `src/utils/format.ts` 里格式化
- 额度：token 整数

---

## 1. 开发

```bash
cd web
pnpm install
pnpm dev            # http://localhost:5273/console/
```

`vite.config.ts` 里已配置 `/console/v1`、`/admin/v1` 的 dev 代理到 `http://127.0.0.1:9999`，
本地起了真实网关时可以直接连（见 §3 关闭 mock）。

## 2. 构建与产物

```bash
pnpm build          # = vue-tsc --noEmit && vite build
pnpm typecheck      # 只做类型检查
pnpm preview        # 预览构建产物
```

产物输出到 **`internal/api/console/dist`**（`build.outDir = '../internal/api/console/dist'`，
`emptyOutDir: true`），由 Go 侧 `go:embed` 打进二进制。

```bash
internal/api/console/dist/index.html
internal/api/console/dist/assets/…
```

资源 `base` 与路由 base 都是 `/console/`，因此静态资源请求为 `/console/assets/…`。

## 3. Mock 开关

**默认就是 mock：没有任何后端也能完整点完所有界面。**

| 环境变量 | 默认 | 说明 |
| --- | --- | --- |
| `VITE_USE_MOCK` | `true` | `false` 时走真实 HTTP；其余值均视为 mock |
| `VITE_API_BASE` | 空 | 网关源站；留空表示同源 |
| `VITE_ADMIN_TOKEN` | 空 | 真实模式下可预置的管理员 JWT（一般不用，走登录页） |

判定逻辑在 `src/api/client.ts` 的 `isMock()`：

- `VITE_USE_MOCK !== 'false'` → mock
- `VITE_USE_MOCK === 'false'` → 真实 axios；此时 admin 请求必须带 token

真实模式示例：

```bash
# .env.local
VITE_USE_MOCK=false
VITE_API_BASE=http://127.0.0.1:9999
```

mock 状态下：

- 右上角/顶栏常驻「mock 数据 · 无后端」角标
- 管理后台自动带一个演示身份（`admin`），不需要登录；点「退出」会记住登出状态，
  之后进 `/admin` 会被路由守卫引导到登录页，而登录页有「以演示身份进入」一键回来
- 所有写操作（建 Key / 吊销 / 调账 / 改渠道 / 换开关 / 注册站点）都在内存里真实生效，
  **刷新页面后重置**（mock 状态不落盘，只有 theme 与登录态写 localStorage）

mock 覆盖了契约里的**全部**接口，包括分页、筛选与幂等：

| 行为 | 说明 |
| --- | --- |
| 创建 Key | 返回 43 位 `sk-` 明文，仅此一次；再查列表只有 `key_prefix` |
| 吊销 Key | `status` 翻成 `revoked`，行变灰、开关禁用 |
| 调账 | 同一 `ref_id` 第二次提交返回 `applied=false`，余额不变 |
| 渠道密钥池 | 只回 `masked`（`****xxxx`）；编辑时留空 = 保留原密钥 |
| 注册/轮换站点 secret | 只在那一次响应里返回明文，列表接口不含 `secret_full` |

## 4. 与后端对接

### 4.1 路由与 mount 路径

单 SPA，两个界面靠**路径前缀**切换布局（`src/App.vue`）：

| 逻辑路径 | 地址栏实际路径 | 布局 |
| --- | --- | --- |
| `/`、`/keys`、`/models`、`/usage`、`/docs` | `/console/…` | `ConsoleLayout` |
| `/admin`、`/admin/channels` … | `/console/admin/…` | `AdminLayout` |
| `/admin/login` | `/console/admin/login` | 独立全屏页 |

因为 `base` 是 `/console/`，router 用 `createWebHistory('/console/')`，路由表里写的是逻辑路径，
base 由 history 实例自动补上。代码里照常 `router.push('/admin/channels')`，地址栏会是
`/console/admin/channels`。

**后端需要做两件事**：

1. `GET /console/*`（除 `/console/v1/**` 之外的 SPA 路由）回落到 `dist/index.html`；
2. `/console/v1/**` 与 `/admin/v1/**` 走真实的 API 处理，不要被 SPA 回落吞掉。

示例（net/http 或任意 router 的伪代码）：

```
if path == "/console" || path == "/console/" { redirect "/console/" }
if strings.HasPrefix(path, "/console/v1/") || strings.HasPrefix(path, "/admin/v1/") { API 处理 }
if strings.HasPrefix(path, "/console/") { // 静态资源优先，未命中则回 index.html
   if fileExists(distPath) { serve }
   else { serve dist/index.html }
}
```

### 4.2 认证

- 用户中心：V10 通过 `GET /console/v10-sso?ticket=…` 下发 HttpOnly `cz_ss` cookie，
  前端不做任何 token 处理，`axios` 已开 `withCredentials: true`
- 管理后台：`POST /admin/v1/login` 拿 `token`，存 localStorage（key `czapi.admin.token`），
  请求 `src/api/client.ts` 自动加 `Authorization: Bearer <token>`

### 4.3 响应信封

所有接口统一 `{"code":0,"msg":"ok","data":{…}}`。
`src/api/client.ts` 的 `unwrap()` 负责拆封：`code !== 0` 抛 `ApiError(code, msg)`，
HTTP 401/403/404/429/5xx 也会被映射成中文提示，页面 `catch` 后统一 `ElMessage.error`。

### 4.4 契约里"不足以支撑界面"的地方

以下为前端在契约留白处的取舍，后端若补齐可去掉对应兜底：

1. **`GET /admin/v1/plans` 没有明确的 PATCH**。前端实现了 `updatePlan` 走
   `PATCH /admin/v1/plans/{plan_code}`（编辑场景），新建走 `POST /admin/v1/plans`。
2. **功能开关没有删除语义**。`PUT /admin/v1/flags/{key}` 只收 `{value, scope}`，
   所以「对齐全局」= 把覆盖 scope 写成当前全局值（UI 里已明确说明）。
3. **`GET /console/v1/keys` 未写明是否分页**。前端同时兼容 `{items}` 与 `{total,items}`；
   用户中心默认一次拉全量（Key 数量受 `plan.max_keys` 限制）。
4. **`GET /admin/v1/channels`、`/models`、`/sites`、`/plans`** 同理，兼容数组与分页对象。
5. **`GET /console/v1/usage/logs` 的 `from`/`to` 是 unix 秒**；界面用日期选择器，
   提交前转成当天 00:00:00 与 23:59:59。
6. **`/admin/v1/accounts/{id}` 详情**契约只写了"含订阅、Key、近 30 天用量"，未给字段名。
   前端假定为 `subscriptions[]`、`keys[]`、`usage_30d{requests,prompt_tokens,completion_tokens,cost_micros}`，
   并把可选的 `trend[]` 用同日趋势图展示（缺省时降级为不显示图表）。
7. **`/admin/v1/settings` 是自由结构**，前端递归展平成 `group.key` 两列只读表，不假设字段名。

## 5. 目录

```
web/
├── index.html                  # /console/ 入口
├── vite.config.ts              # base=/console/，outDir=../internal/api/console/dist
├── tsconfig.json               # strict，noUnusedLocals
├── env.d.ts                    # import.meta.env 类型
├── pnpm-workspace.yaml         # pnpm 10 依赖构建白名单（esbuild 等 postinstall）
└── src/
    ├── main.ts                 # 注册 Pinia / Router / Element Plus(zh-cn) / 样式顺序
    ├── App.vue                 # 按路径前缀选布局
    ├── types/api.ts            # ★ 契约的 TS 镜像：Envelope / Paged / 全部 DTO
    ├── api/
    │   ├── client.ts           # axios 实例 + mock 开关 + 信封拆解 + 错误映射
    │   ├── console.ts          # /console/v1（mock ↔ 真实 二选一）
    │   ├── admin.ts            # /admin/v1
    │   └── mock.ts             # ★ 全接口 mock：确定性伪随机、分页/筛选/幂等
    ├── stores/                 # Pinia：auth / overview / keys / dashboard
    ├── layouts/                # ConsoleLayout（用户中心）/ AdminLayout（后台）
    ├── components/             # PageHeader / StatCard / QuotaMeter / LineChart / EmptyState / CopyButton / Icon
    ├── views/console/          # 概览 / Key / 模型 / 用量 / 文档
    ├── views/admin/            # 登录 / 仪表盘 / 渠道 / 模型定价 / 套餐 / 账户 / 订阅 / 流水 / 对账 / 开关 / 审计 / 系统
    ├── styles/                 # tokens.css（设计变量）/ element-theme.css（EP 覆盖）/ global.css（原语）
    └── utils/                  # format.ts（金额/时间/复制/下载）/ theme.ts（明暗 + 图表色板）
```

## 6. 设计说明（给后续改动的人）

- **配色**：墨蓝表面（`--bg-*`）+ 单一暖琥珀强调色（`--accent`）承担"额度/花费"语义，
  冷钢蓝（`--steel`）只用于对照序列与次要数据。明暗双主题都在 `tokens.css` 里定义。
- **字体**：`-apple-system, "PingFang SC", "Microsoft YaHei"` 等中文字体栈；
  **所有数字、`sk-` 前缀、ID、token 数量一律等宽**（`--font-mono` + `tabular-nums`），
  表格数字列右对齐（`.col-num`）。
- **边框而非阴影**：卡片 `1px solid var(--line-1)`，阴影只留给弹层。
- **额度条**（`QuotaMeter.vue`）是仪表不是装饰：填充 = **已消耗**，
  70% 转暖、90% 转红，扫一眼就能看出谁要超限。
- **Element Plus 已被改主题**：`src/styles/element-theme.css` 用 CSS 变量把 EP 的
  主色、圆角、边框、表格行高、分页、弹窗、消息提示全部对齐本设计系统，
  没有停留在默认外观。
- **不缺空状态**：每个数据视图都有 skeleton（`.sk`）、错误态（可重试）、空态（说明+下一步动作）。
- 响应式到 ~1180–1280px；后台宽表允许横向滚动（`fixed="right"` 固定操作列）。

## 7. 已知限制

- mock 状态只在内存里，**刷新即重置**（这是刻意的：避免"假数据持久化"造成误解）。
- ECharts 全量引入（`import * as echarts`），单包约 1 MB（gzip 343 KB）。
  如需瘦身可改按需引入 `echarts/core` + 需要的 chart/component。
- Element Plus 全量引入（gzip 335 KB）。未用 `unplugin-auto-import` / `unplugin-vue-components`，
  因为显式 import 对构建稳定性更可控（这也是任务书给的取舍）。
- 明暗主题切换不跟随系统（默认深色，用户选择存 localStorage）。
- 契约中未定义的接口（见 §4.4）在真实后端上可能需要微调。
