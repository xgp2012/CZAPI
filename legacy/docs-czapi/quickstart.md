# 快速上手

三步从零到能发请求。

---

## 1. 启动

```bash
go build -o czapi-server ./cmd/server
./czapi-server
```

不需要 MySQL，不需要 Redis。启动日志会打印一段 **V10 站点密钥**：

```
  V10 site secret for app_id "v10" (store it now, it is not shown again):
  F5wT61forp9kldwqn8stSaXWTy9TV240z2SGwyEY7Ts
```

**立刻复制保存。** 这个密钥只在首次启动打印一次，之后无法再取回明文
（只能轮换）。它是 V10 调用网关时签名用的。

如果想让控制台里有演示数据（套餐/模型/渠道），用配置文件启动：

```yaml
# dev.yaml
bootstrap:
  demo_content: true
```

```bash
./czapi-server -config dev.yaml
```

---

## 2. 登录管理后台

浏览器打开 **http://127.0.0.1:9999/console/admin/login**

| 用户名 | 密码 |
| --- | --- |
| `admin` | `admin` |

这是首次启动自动创建的。**任何对外暴露之前必须改掉**
（`bootstrap.admin_password` 改成空字符串可禁止再次自动创建）。

登录后 12 个页面全部可用：仪表盘、渠道管理、模型与定价、套餐、账户与 Key、
订阅、财务流水、对账、功能开关、审计日志、系统设置。

> 用户中心（`/console/`）走 V10 单点登录，直连访问会提示需要票据。
> 那是给终端客户用的，管理员请走 `/console/admin/login`。

---

## 3. 对接 V10

### 3.1 在网关侧登记站点

如果是配置文件方式（推荐，密钥可控）：

```yaml
internal:
  sites:
    - app_id: "v10-prod"
      secret: "<自己生成 64 位 hex，openssl rand -hex 32>"
      name: "生产 V10"
      enabled: true
```

或者用首次启动自动生成的那个 `v10` 站点（密钥见启动日志）。

### 3.2 部署 V10 插件

把 `v10/` 下三个目录放到 V10 站点对应位置：

| 源目录 | 目标位置 |
| --- | --- |
| `v10/idcsmart_llm_gateway/` | `public/plugins/addon/` |
| `v10/llm_gateway_module/` | `public/plugins/server/` |
| `v10/czapi_epay/` | `public/plugins/gateway/` |

然后在 V10 后台「应用」里安装 `idcsmart_llm_gateway`。
详细步骤与配置项见 `v10/README.md`。

### 3.3 在 V10 后台填网关地址与密钥

V10 插件配置里需要填：

- **网关地址**：`http://<网关 IP>:9999`
- **App ID**：`v10`（或你自定义的 `v10-prod`）
- **App Secret**：启动日志里那串，或你自己配的那串

### 3.4 验证连通

```bash
curl http://<网关 IP>:9999/internal/v1/health
# {"code":0,"msg":"ok","data":{"status":"ok","db":"ok","cache":"ok","queue":"ok"}}
```

这个端点**不需要签名**，专用于连通性排查。返回 `ok` 说明网络通了。
若 V10 后台报签名错误，比对 App ID / Secret 是否与网关一致。

---

## 4. 配一个真实渠道

演示渠道里的 key 是占位符 `sk-REPLACE-ME`，必须换成真实 key。

1. 登录 `/console/admin/channels`
2. 编辑 `demo-openai`（或新建一个）
3. 填真实 **Base URL**（例如 `https://api.openai.com`）
4. 在密钥池里填入真实上游 key
5. 点「测试连通性」

> 上游 key 是加密存储的，界面只回显尾 4 位，不会再显示明文。

---

## 5. 发第一个请求

### 5.1 建账户与 Key

管理后台没有直接建 Key 的入口——**Key 由 V10 侧开通**（这是刻意的：
客户身份、套餐、额度都归 V10 管）。

本地想直接试，可以用管理后台的账户/订阅页面手工造一条，或走内部接口：

```bash
# 1. 同步一个 V10 客户
curl -X POST http://127.0.0.1:9999/internal/v1/accounts/sync ...

# 2. 开通订阅（会返回明文 Key）
curl -X POST http://127.0.0.1:9999/internal/v1/subscriptions ...
```

签名方式见 `docs/internal-api-v1.md` §3（有 PHP 参考实现）。

### 5.2 调用

```bash
curl http://127.0.0.1:9999/v1/chat/completions \
  -H "Authorization: Bearer sk-你的密钥" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [{"role":"user","content":"你好"}]
  }'
```

兼容 OpenAI SDK，改 `base_url` 即可：

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:9999/v1",
    api_key="sk-你的密钥",
)
print(client.chat.completions.create(
    model="gpt-4o-mini",
    messages=[{"role": "user", "content": "你好"}],
).choices[0].message.content)
```

---

## 6. 确认计费生效

发完请求后：

1. `/console/admin/dashboard` 应该看到请求数与花费
2. `/console/admin/ledger` 有一笔 `consume` 流水
3. `/console/admin/accounts` 里该账户余额减少了

没有扣费？检查 `/console/admin/flags` 里 `billing_enforced` 是否为 `true`。
为 `false` 时全站只计量不扣费（这是应急开关）。

---

## 常见卡点

| 现象 | 原因 | 解决 |
| --- | --- | --- |
| `/console/` 提示需要票据 | 那是客户中心，走 V10 SSO | 管理员用 `/console/admin/login` |
| 登录 401 | 密码不是 `admin` 了 | 首次启动已建，不会重置；查库或删库重来 |
| `/v1` 返回 401 | Key 不对 | Key 只显示一次，丢了就重新开通 |
| `/v1` 返回 403 `model_not_allowed` | 模型未登记或未启用 | 管理后台「模型与定价」里加 |
| `/v1` 返回 402 `insufficient_quota` | 余额或额度不足 | 管理后台调账，或 V10 侧充值 |
| `/v1` 返回 502 | 上游不可达 | 渠道管理里测连通性，检查 key |
| V10 报签名错误 | App ID / Secret 不匹配 | 两边比对，注意 Secret 只显示一次 |
| 重启后数据全没了 | 用的是内存驱动 | 生产换 `storage.driver: mysql` |

---

## 下一步

- 部署到生产：`docs/runbook.md`
- 接口契约：`docs/internal-api-v1.md`、`docs/console-api-v1.md`
- V10 插件细节与待验证项：`v10/README.md`
