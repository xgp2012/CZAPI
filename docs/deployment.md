# 部署与运维手册

## 组件

| 组件 | 位置 | 作用 |
| --- | --- | --- |
| 网关 | `gateway/`（new-api 二开） | 鉴权、转发、计费、额度账本 |
| V10 插件 | `v10-plugin/` | 让 V10 把网关当成一种可售商品 |
| 契约 | `docs/internal-v10-contract.md` | 双方唯一的接口约定 |

> 要改网关代码，看 [development.md](development.md)；本文只管部署与排障。

## 三种部署形态，先选一种

| 形态 | 适用 | 耗时 | 数据库 |
| --- | --- | --- | --- |
| **A. 单机试跑** | 先看看能不能跑通、做验收 | 2 分钟 | SQLite（零依赖） |
| **B. 单机生产** | 一台服务器，最省事 | 15 分钟 | SQLite 或 PostgreSQL |
| **C. Docker** | 已有容器平台 / 要多副本 | 20 分钟 | PostgreSQL / MySQL |

三种形态跑的是**同一个二进制**，区别只在启动参数与数据库。建议先走 A 把链路验通，再上 B 或 C。

---

## 0. 构建（三种形态都要先做）

> 已有 Linux 二进制可跳过本节。发布产物请连同 `LICENSE`、`NOTICE`、
> `THIRD-PARTY-LICENSES.md` 一起分发——AGPL-3.0 要求。

### 依赖

| 工具 | 版本 | 说明 |
| --- | --- | --- |
| Go | ≥ 1.26.10 | `go.mod` 声明 1.26.10，实测 1.27.1 可编 |
| Bun | ≥ 1.4 | 前端包管理器（上游指定的唯一选择） |
| Node | ≥ 20 | Bun 已包含，通常不用单独装 |

### 关键前提：前端必须先构建

二进制里嵌了**两套**前端，都必须先构建好：

| 前端 | 源码 | 产物 | 访问路径 |
| --- | --- | --- | --- |
| 客户控制台（React，含「V10 运营」分组） | `gateway/web/` | `gateway/web/dist` | `/console/` |
| 旧运维后台（Vue3 + Element Plus，兼容保留） | `gateway/admin-ui/` | `gateway/adminui/dist` | `/admin/` |

`main.go` 里是 `//go:embed web/dist`，`adminui/adminui.go` 里是 `//go:embed all:dist`。
**任一产物不存在时 Go 根本编不过**，不是"跑起来页面空白"，而是编译期直接失败。
所以顺序不能反：

```bash
cd gateway

# 1) 构建客户控制台（产出 web/dist，约 64MB / 1841 个文件）
cd web
bun install --frozen-lockfile
DISABLE_ESLINT_PLUGIN='true' VITE_REACT_APP_VERSION=$(cat ../VERSION) bun run build
cd ..

# 2) 构建运维后台（产出 adminui/dist）
cd admin-ui
bun install
bun run build
cd ..

# 3) 再编译后端（把两个 dist 都嵌进二进制）
CGO_ENABLED=0 go build \
  -ldflags "-s -w -X 'github.com/QuantumNous/new-api/common.Version='$(cat VERSION)" \
  -o czapi-gw .
```

产物是**单个静态二进制**（约 131MB，已含全部前端资源），不需要另外部署静态文件。

> 也可以用 `make build-web` 代替第 1 步。
> `gateway/Dockerfile` 会把这两步都做掉，见形态 C。
>
> 仓库里已提交 `adminui/dist`，所以拿现成源码直接 `go build` 也能过；
> 只有在改动 `admin-ui/` 里的源码时才需要重新执行第 2 步。

### 交叉编译到 Linux

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -ldflags "-s -w -X 'github.com/QuantumNous/new-api/common.Version='$(cat VERSION)" \
  -o czapi-gw-linux .
```

`CGO_ENABLED=0` 很重要：SQLite 用的是纯 Go 实现（`modernc`），
关掉 CGO 才能得到真正自包含的二进制。

---

## 形态 A：单机试跑（SQLite，零依赖）

不需要装数据库、不需要配置文件。

```bash
cd /opt/czapi          # 随便一个目录，SQLite 文件会落在这里
PORT=10000 \
SESSION_SECRET='change-me-to-a-long-random-string' \
V10_BOOTSTRAP_APP_ID=v10test \
V10_BOOTSTRAP_APP_SECRET='至少32位随机串-abcdefghijklmnop' \
V10_CONSOLE_BASE_URL=http://127.0.0.1:10000 \
./czapi-gw
```

首次启动的日志里会看到：

```
[SYS] SQL_DSN not set, using SQLite as database
[SYS] v10: bootstrap site "v10test" created; configure the same app_id/secret in the V10 plugin
[SYS] New API v1.0.0-v10.1 started
```

之后工作目录下出现三个文件：`one-api.db`、`one-api.db-wal`、`one-api.db-shm`。
**备份必须三个一起拷**（SQLite 的 WAL 模式把最近写入放在 `-wal` 里，
只拷主文件会丢最后一段数据）。停机备份最稳：

```bash
sqlite3 one-api.db ".backup '/backup/czapi-$(date +%F).db'"
```

接着跳到 [第 2 步：初始化管理员](#2-初始化管理员)。

---

## 形态 B：单机生产（systemd + PostgreSQL）

### 1. 建库建用户

```bash
sudo -u postgres psql <<'SQL'
CREATE USER czapi WITH PASSWORD '换成强密码';
CREATE DATABASE czapi_gw OWNER czapi;
SQL
```

### 2. 放置文件

```bash
sudo mkdir -p /opt/czapi /var/lib/czapi
sudo cp czapi-gw /opt/czapi/
sudo cp LICENSE NOTICE THIRD-PARTY-LICENSES.md /opt/czapi/     # AGPL 义务
sudo useradd -r -s /usr/sbin/nologin czapi
sudo chown -R czapi:czapi /var/lib/czapi
```

### 3. 写配置

`/etc/czapi.env`（权限设成 `600`，里面有密钥）：

```ini
PORT=10000
SESSION_SECRET=换成长随机串
SQL_DSN=postgres://czapi:密码@127.0.0.1:5432/czapi_gw?sslmode=disable

V10_BOOTSTRAP_APP_ID=v10test
V10_BOOTSTRAP_APP_SECRET=换成32位以上随机串
V10_CONSOLE_BASE_URL=https://api.example.com

GIN_MODE=release
TZ=Asia/Shanghai
```

> `V10_CONSOLE_BASE_URL` 必须是**客户浏览器能访问的地址**（SSO 登录链接基于它拼接），
> 不是容器内网地址。写错了表现为"点登录跳不过去"。

### 4. systemd 常驻

`/etc/systemd/system/czapi.service`：

```ini
[Unit]
Description=CZAPI LLM Gateway
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=czapi
Group=czapi
WorkingDirectory=/var/lib/czapi
EnvironmentFile=/etc/czapi.env
ExecStart=/opt/czapi/czapi-gw
Restart=always
RestartSec=5

# SQLite 时这个目录要可写；PostgreSQL 下仍用作日志/临时目录
ReadWritePaths=/var/lib/czapi

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true

[Install]
WantedBy=multi-user.target
```

```bash
sudo chmod 600 /etc/czapi.env
sudo systemctl daemon-reload
sudo systemctl enable --now czapi
sudo systemctl status czapi
sudo journalctl -u czapi -f        # 看日志
```

`WorkingDirectory` 是 sqlite 文件与运行期文件的落点，别设成 `/opt/czapi`（那里不该可写）。

### 5. 反向代理（Nginx）

```nginx
server {
    listen 443 ssl http2;
    server_name api.example.com;

    ssl_certificate     /etc/letsencrypt/live/api.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/api.example.com/privkey.pem;

    client_max_body_size 64m;

    location / {
        proxy_pass http://127.0.0.1:10000;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # 流式响应（SSE）必须关掉缓冲，否则模型输出会整段卡住
    location ~ ^/(v1|api)/ {
        proxy_pass http://127.0.0.1:10000;
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 600s;
        chunked_transfer_encoding on;
    }
}
```

> **流式输出是最容易部署失败的点。** 少了 `proxy_buffering off`，
> 客户会看到"转半天才一次性吐出全部内容"。另外 `/internal/v10/*` 走的是
> 普通 location 即可，不需要单独开代理。

若前面还有一层代理，把它的网段写进 `TRUSTED_PROXIES`，否则取不到真实 IP。

---

## 形态 C：Docker

上游的 `docker-compose.yml` 用的是 `calciumion/new-api:latest` 镜像——
**那个镜像里没有本项目的 V10 对接层**，直接用它部署等于没部署。必须自己构建：

```bash
cd gateway
docker build -t czapi-gw:v1.0.0-v10.1 .
```

仓库里的 `Dockerfile` 已经做了两步构建（bun 编前端 → go 编后端），无需改动。

`docker-compose.prod.yml`：

```yaml
services:
  czapi:
    image: czapi-gw:v1.0.0-v10.1
    container_name: czapi-gw
    restart: always
    ports:
      - "10000:10000"
    volumes:
      - czapi_data:/data
    environment:
      - SQL_DSN=postgres://czapi:换成强密码@postgres:5432/czapi_gw?sslmode=disable
      - SESSION_SECRET=换成长随机串
      - V10_BOOTSTRAP_APP_ID=v10test
      - V10_BOOTSTRAP_APP_SECRET=换成32位以上随机串
      - V10_CONSOLE_BASE_URL=https://api.example.com
      - GIN_MODE=release
      - TZ=Asia/Shanghai
    depends_on:
      postgres:
        condition: service_healthy

  postgres:
    image: postgres:18
    restart: always
    environment:
      - POSTGRES_USER=czapi
      - POSTGRES_PASSWORD=换成强密码
      - POSTGRES_DB=czapi_gw
    volumes:
      - pg_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U czapi -d czapi_gw"]
      interval: 5s
      timeout: 5s
      retries: 10

volumes:
  czapi_data:
  pg_data:
```

```bash
docker compose -f docker-compose.prod.yml up -d
docker compose -f docker-compose.prod.yml logs -f czapi
```

镜像是 `debian:bookworm-slim` 基础（非 alpine），因为上游需要 `libasan8`。

> **多副本前先读"已知限制"。** nonce 去重是进程内的，多副本要么做站点粘性路由，
> 要么接受极低概率的 nonce 双通过（签名本身仍安全）。

---

## 上线顺序

顺序不能乱。每一步都依赖前一步的产物。

### 1. 启动网关

必需环境变量：

| 变量 | 说明 |
| --- | --- |
| `PORT` | 监听端口 |
| `SESSION_SECRET` | 会话签名密钥，随机长串 |
| `SQL_DSN` | 不设则用内置 SQLite（适合单机）；生产建议 MySQL/PostgreSQL |
| `V10_BOOTSTRAP_APP_ID` | 首个站点的 app_id |
| `V10_BOOTSTRAP_APP_SECRET` | 首个站点的 secret，建议 32 位以上随机 |
| `V10_CONSOLE_BASE_URL` | 控制台对外地址，SSO 登录链接基于它拼接 |
| `V10_SECRET_ENC_KEY` | 可选。用于加密库中 secret；不设则回落到 `SESSION_SECRET` 派生 |

`SQL_DSN` 三种写法（不设则为内置 SQLite，适合单机与试跑）：

```
# SQLite（零依赖，文件位于工作目录 one-api.db）
（不设 SQL_DSN）

# MySQL ≥ 5.7.8 —— 库须为 utf8mb4，否则启动时会拒绝（中文无法存储）
SQL_DSN=user:pass@tcp(127.0.0.1:3306)/czapi_gw?charset=utf8mb4&parseTime=True&loc=Local

# PostgreSQL ≥ 9.6
SQL_DSN=postgres://user:pass@127.0.0.1:5432/czapi_gw?sslmode=disable
```

> MySQL 的两点注意：
> ① 库的默认字符集/排序规则必须是中文可用的（`utf8mb4` / `utf8mb4_general_ci` 等），
>    否则网关启动时 `checkMySQLChineseSupport` 会直接报错退出——这是上游的保护，
>    不是本项目加的。
> ② 若实例 `lc_messages` 被设成非英文，唯一冲突的错误文本会变。本项目已按
>    SQLSTATE/错误码识别（见下文"已知限制"），不依赖文本语言；但把 `lc_messages`
>    固定为 `C` 仍是更省心的做法。

首次启动会自动完成三件事：

1. 建 V10 相关表（`v10_sites` / `v10_accounts` / `v10_refs` / `v10_hosts` / `v10_login_tickets`）。
2. 强制关闭面向最终用户的注册、密码注册、邮箱验证与自助充值入口。
3. 按 `V10_BOOTSTRAP_APP_ID` / `V10_BOOTSTRAP_APP_SECRET` 注册引导站点（已存在则跳过）。

> 第 2 步是项目的立身之本。它不依赖任何人工操作，因为"某次重装数据库后忘了关"
> 会直接变成一个可以白嫖的注册口子。

**注意：光靠开关还不够。** new-api 的 `/api/user/topup` 实际是**卡密兑换**，
只由"合规已确认"这一个条件把守；而运维为了建订阅套餐**必须**先确认合规。
也就是说在真实部署状态下，这条路径对所有登录用户都是开着的。

实测过：V10 客户拿着 SSO 会话，用一张真实网关卡密成功给自己加了 500000 额度，
完全绕过 V10 的订单。因此网关额外做了**身份判定**拦截
（`internalv10/guard.go`）：凡由 V10 托管的账户，一律禁止使用网关自带的
付费、兑换、订阅自购入口，包括 `/api/subscription/balance/pay`——它直接用
网关余额扣款，客户可以拿 V10 下发的额度在网关再买一份订阅。

这套拦截由验证套件第 5 项守护（25 条断言），改动路由时不要漏挂。

### 2. 初始化管理员

运维面已并入客户控制台，**日常运维不再需要单独登录一套账号**：

| 用途 | 入口 | 账号来源 |
| --- | --- | --- |
| **日常运维 + 建套餐/渠道/模型** | `/console/` | 安装向导创建，或 `/api/setup` 初始化 |
| 旧的独立运维后台（保留兼容） | `/admin/` | 网关首次启动自动创建，默认 `admin` / `admin`，**强制改密** |

`/console/` 侧边栏有「V10 运营」分组，包含概览、站点、托管账户、账本、Key、
订阅与用量七页，与 new-api 原生页面共用同一套会话。旧的 `/admin/` 仍可访问
（走 `czapi_admin` Cookie），但功能已在控制台侧复刻，新部署直接用 `/console/`。

#### 2.1 客户控制台 `/console/`（推荐）

首次打开会进入安装向导：数据库检查 → 管理员账号 → 使用模式 → **V10 站点（可跳过）**
→ 确认初始化。在「V10 站点」填 App ID，初始化时会顺手建好站点并把明文密钥显示
**一次**，照抄进 V10 插件即可；留空则之后到「V10 运营 → 站点」补建。

#### 2.2 旧运维后台 `/admin/`（兼容保留）

浏览器直接打开：

```
http://<服务器IP>:10000/admin/
```

首次登录用 `admin` / `admin`，登录后会被强制跳到改密页，改完才能进其它页面。

如果 `/admin/` 会暴露到公网，**请在第一次启动前**就在 `/etc/czapi.env` 里设好口令，
否则任何人都可能先于你登录并改掉密码，把你锁在外面：

```bash
ADMIN_BOOTSTRAP_USERNAME=admin
ADMIN_BOOTSTRAP_PASSWORD=<你的强口令>
```

这两个变量**只在创建账号那一次生效**；账号已存在时不会覆盖，所以运维改过的密码不会被重启重置。

后台能做的事：

- **站点管理** — 新建站点、轮换密钥（旧的立即失效）、停用/启用（停用后该站点所有签名请求 403，用于紧急止血）
- **账户与额度** — 按客户号或用户名查余额，手工加额/扣额（正负金额，写入账本并记录操作人）
- **账本对账** — `v10_refs` 流水，按来源、凭证 ID、时间区间筛选
- **Key 管理** — 按客户查看 Key、吊销（置为禁用而非删除，历史用量记录仍可追溯）
- **Host 与订阅** — 查看 host 状态，暂停/恢复/终止
- **用量报表** — 按客户、按模型聚合区间消费

单笔调额上限约 **4294.97 元**（受 `users.quota` 字段 32 位存贮限制），超过请拆成多笔，
每笔都会独立记账。

#### 2.2 new-api 原生后台（建套餐时才需要）

```bash
curl -X POST http://127.0.0.1:10000/api/setup \
  -H 'Content-Type: application/json' \
  -d '{"username":"rootadmin","password":"<强密码>","confirmPassword":"<强密码>"}'
```

注意：

- 用户名 **最多 12 字符**，密码确认字段名是 `confirmPassword`。
- 这个接口会写选项表，**可能把第 1 步的锁面冲掉**。网关已在接口末尾自动重新施加约束；
  如果你改了这个接口，记得保留那次重施。
- V10 客户没有可用密码，所以"密码登录开着"只对运维有效，不构成注册/登录口子。

### 3. 确认付款合规并建套餐

new-api 要求管理员显式确认合规条款，之后才允许建套餐（`403` 会提示这一点）：

```bash
# 1) 管理员登录拿 token
curl -X POST http://127.0.0.1:10000/api/user/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"rootadmin","password":"<密码>"}'

# 2) 确认合规
curl -X POST http://127.0.0.1:10000/api/option/payment_compliance \
  -H "Authorization: Bearer <token>" -H 'Content-Type: application/json' \
  -d '{"confirmed":true}'

# 3) 建套餐（记下返回的 id，插件配置要用）
curl -X POST http://127.0.0.1:10000/api/subscription/admin/plans \
  -H "Authorization: Bearer <token>" -H 'Content-Type: application/json' \
  -d '{"plan":{"title":"基础版","price_amount":99,"currency":"USD",
       "duration_unit":"month","duration_value":1,"total_amount":50000000,"enabled":true}}'
```

`total_amount` 是套餐额度（new-api quota 单位），`50000000` ≈ 100 元额度
（`QuotaPerUnit = 500000`，即 1 元 = 50 万 quota）。

### 4. 安装并配置 V10 插件

#### 4.1 部署插件文件

把两个目录放到 V10 站点根下（**插件必须先装，模块依赖它的配置**）：

```bash
# 插件（用户自助面 + 后台运营面 + 网关客户端）
cp -r v10-plugin/idcsmart_llm_gateway   /path/to/v10/public/plugins/addon/

# 模块（把 LLM 套餐做成 V10 可售商品）
cp -r v10-plugin/llm_gateway          /path/to/v10/public/plugins/server/

# 权限：与同目录其它插件保持一致
chown -R www-data:www-data /path/to/v10/public/plugins/addon/idcsmart_llm_gateway
chown -R www-data:www-data /path/to/v10/public/plugins/server/llm_gateway
```

然后在 V10 后台「插件管理」里安装 `LLM 网关`，再到「模块管理」安装 `LLM 网关模块`。
安装时会自动建五张表（`idcsmart_addon_idcsmart_llm_gateway_{key,usage,pushlog,txn,redeem}`）。

#### 4.2 填配置

在 V10 后台「插件 → LLM 网关 → 配置」：

| 配置项 | 填什么 |
| --- | --- |
| 网关地址 | `https://api.example.com`（**不要**带 `/internal/v10` 后缀，也不要结尾斜杠） |
| App ID | 同 `V10_BOOTSTRAP_APP_ID` |
| App Secret | 同 `V10_BOOTSTRAP_APP_SECRET` |
| 套餐映射 | `{"V10商品ID": 网关套餐ID}`，值是**数字**，如 `{"12":1,"13":2}` |
| 每产品 Key 上限 | 单 host 可建 Key 数 |
| 调用超时 | 建议 ≥ 5000ms |
| 回调密钥 | **留空**。当前契约没有网关→V10 的反向回调 |

填完点「测试连接」——它打的是免签名的 `/internal/v10/health`，
如果这里都不通，后面的签名接口更不可能通。

> 常见填错：网关地址多写了 `/internal/v10`。结果所有请求变成
> `/internal/v10/internal/v10/health`，返回 404，而界面只提示"连接失败"。

#### 4.3 确认计划任务已挂上

V10 的插件 hook 需要已注册，否则失败重投不会自动跑：

| Hook | 频率 | 作用 |
| --- | --- | --- |
| `order_paid` | 事件 | 支付成功 → 加额度 |
| `after_host_delete` | 事件 | 产品删除 → 终止订阅、吊销 Key |
| `five_minute_cron` | 5 分钟 | 失败推送重投 |
| `daily_cron` | 每日 | 失败重投 + 用量快照 |

在 V10 后台的计划任务列表里确认 `five_minute_cron` / `daily_cron` 在跑。
**`order_paid` 的事件名必须在你的 V10 实例上核实过**——开发文档未逐页确认，
若实例里叫别的名字，改 `v10-plugin/idcsmart_llm_gateway/hooks.php` 里的字符串即可
（回调体不用动）。这是上线前唯一必须人工核对的事件。

### 5. 验收

```bash
php scripts/v10-verify-all.php http://127.0.0.1:10000 <app_id> <secret>
```

六个套件，全部通过才算可用：

| 套件 | 覆盖 |
| --- | --- |
| 运维流程 | 管理员登录、合规、建套餐、站点接口（11 项） |
| 插件契约 | 插件里每个 `LlmApi` 方法与网关逐一对齐（36 项） |
| host 生命周期 | 开通 → 续期 → 暂停 → 恢复 → 终止，以及 Key 联动（16 项） |
| SSO 会话 | 票据签发 → 换取会话 → 真正能调控制台接口 |
| 用户面封锁 | V10 客户用**真实卡密**也无法自助充值/兑换/买订阅（25 项） |
| 密钥轮换 | 轮换后旧密钥立即失效、停用站点拒绝请求（14 项） |

合计 102 条断言。任一失败都会打印 `FAIL` 行并给出实际值。

> 第 5 个套件必须放在运维流程**之后**跑：它验证的正是"合规已确认"这个
> 真实上线状态。合规没确认时支付/兑换本来就被合规开关挡着，
> 那时候测会得出"已经关好了"的假结论。

## 日常运维

### 站点管理

后台 API（需管理员会话）：`/api/v10/admin/*`，详见契约文档。
常用动作：

- **轮换密钥**：`POST /api/v10/admin/sites/:id/secret`。
  轮换后**旧密钥立即失效**，必须同步改 V10 插件配置，否则插件全部请求 401。
  建议在低峰期做，并在插件里先用 `app_secret_prev` 承接过渡。
- **停用站点**：`POST /api/v10/admin/sites/:id/status {"enabled":false}`。
  该站点全部签名请求返回 `403`，用于紧急止血。

### 对账

网关不碰钱，所以对账只看两处：

1. **V10 侧订单/卡密流水**：客户实际付了多少。
2. **网关 `v10_refs` 表**：每条 `(site_id, ref_type, ref_id)` 落一行，
   含 `delta_quota` 与 `balance_after`。

```sql
SELECT ref_type, ref_id, delta_quota, balance_after, created_at
FROM v10_refs WHERE site_id = 1 ORDER BY id DESC LIMIT 50;
```

按 `ref_id` 两边对，数量与金额都要一致。对不上的常见原因：

- V10 发出了通知但网关全部重试失败 → 插件 `push_log` 表里有 `failed` 记录，
  跑一次 `retryFailed` 补推。
- 手工改过账 → `kind = admin_adjust` 的行就是痕迹。

### 退款

调用 `POST /credit`，`kind=refund`，金额为负，`ref_type`/`ref_id` 用退款单号。
不要复用原订单的 `ref_id`，否则会被幂等挡掉。

### 客户欠费

V10 侧应调用 host 暂停：

```json
{ "client_id": 2001, "status": "suspended", "reason": "overdue" }
```

网关会连带停用该客户全部 Key。补缴后 `status=active` 恢复。
注意恢复只拉回"被停用"的 Key，**不会**复活已吊销的。

## 故障排查

| 现象 | 先查什么 |
| --- | --- |
| 插件所有请求 401 | app_secret 是否与网关站点一致；是否刚轮换过密钥；服务器时钟偏差 > 5 分钟 |
| `40102 nonce 重放` | 插件是否复用了 nonce；是否重复发送了同一请求 |
| `40300 站点被停用` | `/api/v10/admin/sites` 看 `status` |
| `40200 余额不足` | 核对 V10 是否漏发充值；`allow_negative` 是否符合预期 |
| 加额度成功但余额没变 | 检查 `ref_id` 是否与历史重复（幂等会静默返回 `applied:false`） |
| 续费后到期时间没变 | 确认 `host_id` 与首次开通一致——不同 `host_id` 会新建而非续期 |
| 套餐恢复了但 Key 还不能用 | 该 Key 可能已吊销（`expired`），恢复只处理被停用的 |
| SSO 进去白屏 | 票据 60 秒过期或已被使用；`V10_CONSOLE_BASE_URL` 是否指向实际控制台地址 |
| 客户能看到注册/充值入口 | 检查 `/api/status` 里 `register_enabled` / `self_use_mode_enabled`；若被改过，重启网关会重新加锁 |

### 装不上 / 起不来（install.sh 相关）

| 现象 | 原因与处理 |
| --- | --- |
| `install.sh` 报“/etc/czapi.env 里仍有未替换的占位符” | 旧版本残留配置里有 `xxxxxx` 之类的示例密码。脚本已自动备份并注释掉坏的 `SQL_DSN`，回落到内置 SQLite。想继续用 PG/MySQL 就设 `KEEP_DSN=1` 重跑，脚本会停下让你填真实密码 |
| 日志 `failed to parse as URL (net/url: invalid userinfo)` | 同一条：`SQL_DSN` 里的密码是字面量 `xxxxxx`，或密码含未转义的特殊字符（`@ : / ?` 必须百分号编码） |
| `restart counter is at 8861` 之类的疯狂重启 | 旧版 unit 没有重启上限。现版加了 `StartLimitIntervalSec=300` + `StartLimitBurst=5`，5 分钟内失败 5 次即停下。更新后执行 `systemctl daemon-reload && systemctl reset-failed czapi` |
| 脚本卡在“等待端口就绪”不动 | 旧版健康检查没有失败出口。现版服务起不来会立刻 `systemctl stop` 并报错退出。若仍在等，查 `journalctl -u czapi -n 50` 与 `ss -lntp \| grep <端口>` |
| 端口和预期不一致 | 脚本从 `/etc/czapi.env` 的 `PORT=` 取实际值，而不是脚本变量。手工改过配置就以配置为准 |
| 忘了后台密码 | `ADMIN_BOOTSTRAP_PASSWORD` 只在**首次创建账号**时生效，不会覆盖已有口令。需要重置时删掉 `v10_admin_accounts` 里对应记录再重启，或直接改库中的 `password_hash` |

排查配置问题的通用命令：

```bash
# 找出配置里所有可疑占位符
grep -nE '__[A-Z_]+__|xxxxxx|<[^>]*>' /etc/czapi.env

# 确认实际生效的端口
grep -m1 '^PORT=' /etc/czapi.env; ss -lntp | grep czapi

# 看服务是否已进入 failed（配置错误的典型状态）
systemctl status czapi --no-pager; systemctl reset-failed czapi
```

## 已知限制

- **数据库验证覆盖 SQLite 与 PostgreSQL，缺 MySQL。** 本项目要求同时兼容
  SQLite / MySQL ≥ 5.7.8 / PostgreSQL ≥ 9.6。已实测：

  | 引擎 | 版本 | 结果 |
  | --- | --- | --- |
  | SQLite | 内置（`modernc`） | 全 6 套件通过 |
  | PostgreSQL | 18.6 | 全 6 套件通过，含重启幂等 |
  | MySQL | — | **未验证**，开发机无实例且无 Docker |

  已覆盖的内容：建表、二次启动幂等（数据与站点保留）、`v10_refs` 唯一约束
  （重复 voucher 只落一行）、事务内的条件 `UPDATE quota + ?`、`lockForUpdate` 分支。
  **但 MySQL 的 `duplicate entry` 分支未在真实实例上跑到过**，上生产前必须补测。

  > **踩过的坑（已修）：** 唯一冲突的识别原先只匹配英文错误文本。
  > 实测的 PostgreSQL 实例 `lc_messages` 是中文，报错是
  > `重复键违反唯一约束 (SQLSTATE 23505)`，于是重放被当成普通错误返回 400——
  > 数据没脏，但 V10 会一直重试，客户看到的是"充值失败"。
  > 现在优先按 SQLSTATE / 驱动错误码识别（与 locale 无关），文本匹配只作兜底，
  > 中英文都覆盖。回归测试见 `internalv10/bridge_test.go` 的
  > `TestIsDuplicateKeyAcrossDialects`。
  > **给运维的提醒：** 若你的数据库 `lc_messages` 是其他语言，请确认
  > 该测试里没有覆盖到的语言不会落到兜底分支之外——最稳的做法是保持默认英文，
  > 或把 `SQL_DSN` 里的 `lc_messages` 固定为 `C`。

  补测方法（照做即可，约 10 分钟）：

  ```bash
  # 1. 准备一个 utf8mb4 的空库
  mysql -uroot -e "CREATE DATABASE czapi_gw CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci;"

  # 2. 指向它并启动
  SQL_DSN='user:pass@tcp(127.0.0.1:3306)/czapi_gw?charset=utf8mb4&parseTime=True&loc=Local' \
  PORT=3240 SESSION_SECRET=xxx \
  V10_BOOTSTRAP_APP_ID=v10test V10_BOOTSTRAP_APP_SECRET=xxx \
  V10_CONSOLE_BASE_URL=http://127.0.0.1:3240 \
  ./czapi-gw

  # 3. 跑全量验收
  php scripts/v10-verify-all.php http://127.0.0.1:3240 v10test xxx

  # 4. 重启一次，确认二次启动幂等
  #    （重启后再跑一次步骤 3，仍应全绿）

  # 5. 直接查库确认约束与幂等
  mysql -uroot czapi_gw -e "SHOW INDEX FROM v10_refs;"
  mysql -uroot czapi_gw -e "SELECT site_id,ref_type,ref_id,COUNT(*) c FROM v10_refs GROUP BY 1,2,3 HAVING c>1;"
  ```

  期望结果：6 个套件全过；`uk_v10_ref` 存在；重复查询返回空集；
  重启后数据与索引都还在。**若全过，MySQL 即可视为已验证**，
  请把实测的 MySQL 版本号补进上面的表格。

  MySQL 相关的代码侧准备已经做完，静态审查覆盖了全部查询：
  - 无 `LIKE`/日期函数/窗口函数，聚合在 Go 侧做，`ORDER BY id` 三库通用；
  - 无原生 SQL，全部走 GORM；`ABS(quota + ?) <= ?` 三库都有；
  - 唯一/索引名显式固定（`uk_v10_ref` 等），不随方言漂移；
  - 列类型用 `varchar(n)`/`bigint`/`text`，UTF-8 与索引长度在 utf8mb4 下均安全；
  - 唯一冲突按 SQLSTATE/错误码识别，`1062` 已在测试里覆盖。
  因此 MySQL 上**预期不需要改代码**，只需实测确认。

  > **单笔入账上限（重要）：** `users.quota` 的列类型是 `type:int`，
  > MySQL 上会建成 32 位 INT（上限 2147483647），而 `common.MaxWalletQuota`
  > 是 2^53-1——两者不是一个数量级。因此 `/internal/v10/credit` 对**单笔**
  > 金额按 int32 上限校验，超过直接返回 40000 并提示拆分：
  > `amount_micros exceeds the largest single credit ... split it into multiple credits`。
  > 这是**故意报错而不是静默饱和**：否则客户付 5000 元只会到账约 4294 元。
  > 折算成人民币，单笔上限约 **4294.97 元**。大额充值请在 V10 侧拆成多笔、
  > 各自带不同 `ref_id`（财务上也更好对账）。
- 多副本部署时 nonce 去重是**进程内**的。单副本无问题；多副本下同一 nonce 可能在不同副本各通过一次。
  签名本身仍安全（nonce 只防重放，不防伪造），但严格的一次性语义需要外置存储。
- 控制台已并入 V10 运营面：`/console/` 侧边栏的「V10 运营」分组提供站点、
  托管账户、账本、Key、订阅与用量六类运维页，与 new-api 原生页面共用同一套
  会话与鉴权。旧的 `/admin/`（Vue）仍保留，但不再是主路径；两套凭据
  （new-api 管理员会话 / `czapi_admin` Cookie）由同一个中间件识别。
  页面品牌文案仍沿用上游默认值，未按本项目做视觉定制。
