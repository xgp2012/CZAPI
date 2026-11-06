# CZAPI 网关开发指南

面向要**改这个网关代码**的人。部署与运维看 [deployment.md](deployment.md)，
V10 插件侧开发看 [v10-plugin/DEVELOPMENT.md](../v10-plugin/DEVELOPMENT.md)，
`/internal/v10` 的报文格式看 [internal-v10-contract.md](internal-v10-contract.md)。

本文只讲三件事：代码在地图上的哪一块、怎么把环境跑起来、以及哪些地方
一改就会在线上静默出错。

---

## 1. 仓库地图

```
gateway/                  Go 模块根（module github.com/QuantumNous/new-api）
  main.go                 //go:embed web/dist —— 客户控制台的静态资源
  adminui/                旧运维后台的 embed 包装（//go:embed all:dist）
  controller/             HTTP 处理器（含 setup.go 安装向导后端）
  router/                 路由装配
  middleware/             鉴权、限流、审计
  model/                  GORM 模型与迁移
  internalv10/            ★ V10 对接层（本文重点）
  web/                    React 控制台（唯一主入口，挂在 /console/）
  admin-ui/               Vue3 旧运维后台（挂在 /admin/，兼容保留）
v10-plugin/               魔方 V10 的 PHP 插件（独立交付物）
docs/                     部署、契约、本文
```

`gateway/` 才是 Go 模块根目录，不是仓库根。`go build` 必须在 `gateway/` 里跑。

---

## 2. 开发环境

### 2.1 工具链

| 工具 | 版本 | 说明 |
| --- | --- | --- |
| Go | 1.26.10 | 见 `gateway/go.mod` |
| Node | 20+ | 仅用于 i18n 等脚本 |
| Bun | 1.4.x | 前端包管理与构建，**不要用 npm/yarn** |
| PHP | 8.x | 只在改 `v10-plugin/` 时需要 |

### 2.2 首次构建

两个前端**必须先构建，否则 Go 编不过**——不是"页面空白"，而是
`//go:embed` 找不到文件，编译期直接失败：

```bash
cd gateway/web      && bun install && bun run build
cd ../admin-ui      && bun install && bun run build
cd ..               && go build -o czapi-gw.exe .
```

### 2.3 启动

```bash
cd gateway
./czapi-gw.exe
```

不设 `SQL_DSN` 就用内置 SQLite（默认 `one-api.db`）。首次启动会自动建表、
锁定用户面、注册引导站点并创建后台账号，然后打开
`http://127.0.0.1:10000/console/` 走安装向导。

### 2.4 迭代时

只改前端时不必反复重编 Go——`bun run dev` 起开发服务器并代理 API。
改完仍然要跑一次 `bun run build` 才能进二进制。

改了 `web/` 的代码后，`routeTree.gen.ts` 由路由插件自动重写，**要一起提交**。

---

## 3. 提交前的检查

按顺序跑，前一步失败就不必往下：

```bash
# 1. 前端类型检查（rsbuild 不做类型检查，必须单独跑）
cd gateway/web && bun x tsc --noEmit -p tsconfig.app.json

# 2. 前端 lint（只查改动文件即可，全仓有既有告警）
bun x oxlint -c .oxlintrc.json src/features/v10 src/features/setup

# 3. 前端构建
bun run build

# 4. 后端
cd .. && go build ./... && go vet ./internalv10/ ./controller/
go test ./internalv10/ ./router/
```

两点容易被误解：

- **`bun run build` 通过 ≠ 类型正确。** rsbuild 只做转译，不做类型检查。
  曾有 `string | null` 的实参错误在构建通过的状态下进了主干。
- **根目录 `tsconfig.json` 是 references 解决方案文件**，直接
  `tsc --noEmit` 什么也不检查。必须指定 `-p tsconfig.app.json`。

---

## 4. V10 对接层（`internalv10/`）

### 4.1 文件职责

| 文件 | 职责 |
| --- | --- |
| `sign.go` | HMAC-SHA256 签名与待签串拼装 |
| `middleware.go` | `SiteAuth()`：校验签名、时间窗、nonce |
| `router.go` | `/internal/v10` 的处理器 |
| `credit.go` | 加/扣额度，幂等落库 |
| `account.go` | 托管账户与 host 生命周期 |
| `key.go` | Key 的创建、改状态、吊销 |
| `site.go` / `secret.go` | 站点注册与密钥加解密 |
| `store.go` | 表定义与常量 |
| `console_api.go` | 运营面接口（`/api/v10/console/*`） |
| `console_auth.go` | ★ 双凭据鉴权中间件 |
| `admin.go` | `/api/v10/admin/*`（new-api 管理员会话） |

### 4.2 双凭据鉴权

融合后运维用 new-api 账号登录控制台，但旧的 `/admin/` 仍用
`czapi_admin` Cookie。两者都由 `RequireConsoleAdmin()` 识别：

```
请求 → 有 czapi_admin Cookie?
        是 → 校验强制改密 + 同源 → 放行（operator = 旧账号名）
        否 → new-api 会话 role >= RoleAdminUser?
               是 → 放行（operator = 会话用户名）
               否 → 401
```

**不要把它改成"识别不出就放行"。** 前端拦截器靠 401 判断会话失效并跳
`/sign-in`；返回 200 会让用户卡在空白页。同理，普通用户（role=1）必须被拒——
运营面能看到所有客户的额度与 Key。

新增运营面接口时，要同步在 `middleware/access_token_routes.go` 声明，
否则 `router/` 下的 `TestAccessTokenRouteRulesCoverEveryDashboardRoute`
会以 "must be declared exactly once" 失败。

### 4.3 金额换算

```
1 元 = 1,000,000 微元（micros）
1 元 = 500,000 new-api quota
```

必须**先乘后除**：`quota = micros * QuotaPerUnit / 1_000_000`。
先除会把小于 2 微元的金额直接抹成 0。

`users.quota` 是 int32，单笔上限约 **4294.97 元**，超了会明确报错而不是
静默饱和——这是故意的，否则客户付 5000 元只会到账 4294 元。

### 4.4 幂等

`v10_refs` 上有 `(site_id, ref_type, ref_id)` 唯一索引。同一凭证重投返回
`applied: false` 且不改余额。新增写入路径时不要绕开这张表。

---

## 5. 前端：新增一个运营页

以「站点」为例，三处改动缺一不可：

**① 加接口** — `web/src/features/v10/api.ts`

```ts
export function getSites() {
  return unwrap<Paged<V10Site>>(api.get(`${BASE}/sites`))
}
```

`unwrap()` 会剥掉 `{success, message, data}` 信封并处理失败分支。

**② 加页面** — `features/v10/components/xxx.tsx`，然后从
`features/v10/index.ts` 导出。表格用 `StaticDataTable`，危险操作确认用
`ConfirmDialog`，复制用 `CopyButton`——**不要手写这些交互**，仓库里已有封装。

**③ 加路由 + 菜单**

```tsx
// routes/_authenticated/v10/xxx.tsx
export const Route = createFileRoute('/_authenticated/v10/xxx')({
  component: RouteComponent,
})
```

再在 `hooks/use-sidebar-data.ts` 的 `v10` 分组里加一项，
`requiredRole: ROLE.ADMIN`。

### 5.1 契约字段只能读后端

前端的 `types.ts` 是手写的，**类型检查不会发现字段名写错**。写之前先看
`console_api.go` 里对应的 handler 返回什么。实际踩过的坑：

- 改 host 状态要传**主键 `id`**，不是 V10 的 `host_id`（后端 `First(&host, id)`）。
- 账本 `kind` 的枚举来自插件 `LlmApi::creditAccount`：
  `v10_order | redeem_code | admin_adjust | refund`。
- host 状态只有 `active / suspended / terminated`，没有 `expired`。

写错不会报错，只会静默返回空列表或改错行。

### 5.2 i18n

所有用户可见文案走 `t('English key')`。新 key 必须同时加进
`web/src/i18n/locales/` 下**全部 7 个**语言文件（en/zh/zh-TW/fr/ru/ja/vi）。

`bun run i18n:sync` 只做排序与归一化，**不会**从源码提取新 key。
加完用下面这段核对键数是否一致：

```bash
cd gateway/web && node -e "
const fs=require('fs');
const ls=['en','zh','zh-TW','fr','ru','ja','vi'];
console.log(ls.map(l=>l+'='+Object.keys(JSON.parse(fs.readFileSync('src/i18n/locales/'+l+'.json','utf8')).translation).length).join(' '));
"
```

---

## 6. 安装向导

`features/setup/` 是 5 步向导，步骤定义在 `setup-wizard.tsx` 的 `STEPS`：

```
数据库检查 → 管理员账号 → 使用模式 → V10 站点（可跳过）→ 确认初始化
```

后端在 `controller/setup.go`。提交 `v10_app_id` 时会在初始化过程中一并建站，
并把**明文密钥回显一次**（库里只存密文）——所以前端弹窗故意不允许点遮罩关闭，
避免部署方没抄下来就丢了。

注意 `PostSetup` 会直接写 `SelfUseModeEnabled`，所以在写完设置后必须再调
`v10.ApplyUserFacingLock()`，否则注册/邮件验证等入口会被重新打开。

---

## 7. 测试

后端测试集中在 `internalv10/bridge_test.go`（**不要为新功能另起文件**）。
数据库用 `newTestDB(t)`，它是临时目录里的真实 SQLite，不是 mock。

已有两个契约级用例，改动相关逻辑时它们应当继续通过：

- `TestDashboardAdminNameGatesOnRole` — 运营面鉴权边界（8 个用例）
- `TestCreateSiteStoresOnlyCiphertext` — 建站只存密文、可解回、重复报冲突

已知的既有失败（与 V10 无关，不要误判为自己改坏了）：

- `TestAccessAndPanicLogsFollowLogRotation`（在 `./middleware/`）

---

## 8. 容易被改坏的地方

1. **`internalv10/sign.go` 的待签串**必须与 PHP 侧 `LlmApi.php::sign` 逐字节
   一致。空格编码用 `%20` 而不是 `+`，行间用 `\n` 且**无尾随换行**。
2. **`access_token_routes.go`** 漏声明会让路由覆盖测试失败。
3. **`//go:embed` 的目录**：`adminui` 单独成包是因为 embed 不能引用包目录之外
   的文件。移动前端产物目录会导致编译失败。
4. **不要用 PowerShell 的 `-replace` 改源码**：会把 UTF-8 写坏。用编辑器或
   `write` 工具。
5. **`Dialog`/`ConfirmDialog` 的 API**：`useDialog()` 返回
   `[open, handlers]` 元组，handlers 只有 `open/close/toggle`，没有 `setOpen`。

---

## 9. 许可与署名

本项目基于 new-api（AGPL-3.0）。**`new-api` / `QuantumNous` 的品牌与署名不得
删除或替换**，包括文件头版权声明、页脚署名、Go module 路径。新增文件沿用
同样的 AGPL 文件头。
