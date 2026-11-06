# legacy/ — 旧版自研实现（已归档，不再维护）

这里存放的是本项目**第一版**的完整实现：一个从零自研的 Go + Vue3 网关。
它已被 `gateway/`（基于 new-api 的二开）整体取代，**不要在生产环境部署本目录**。

保留而非删除，只因为两件事还有参考价值：

1. `internal/` 里的 V10 签名协议、幂等入账、host 生命周期等逻辑，是现在
   `gateway/internalv10/` 的设计来源，对照阅读有助于理解当初的取舍。
2. `v10-plugin-v1/` 是插件的第一版，排查老客户的历史行为时可能用得上。

## 技术栈与现状

| 项 | 旧实现（本目录） | 现役实现（`gateway/`） |
| --- | --- | --- |
| Go 模块 | `czapi`（独立 module） | `github.com/QuantumNous/new-api` 二开 |
| HTTP 框架 | go-chi | Gin |
| ORM | 原生 SQL + `migrations/0001_init.sql` | GORM v2 + AutoMigrate |
| 前端 | `web-console/`（Vue3） | `gateway/web/`（React）+ `gateway/admin-ui/`（Vue3） |
| 模型/计费 | 自研 | 复用 new-api 的渠道、计费、日志体系 |
| 状态 | **已归档** | **现役** |

两者**不能同时运行**，也不共享数据库。旧实现的表结构由
`migrations/0001_init.sql` 定义，与新实现的 `v10_*` 表不同。

## 目录说明

| 路径 | 内容 |
| --- | --- |
| `cmd/server/main.go` | 旧实现的入口 |
| `internal/` | 业务逻辑：签名校验、额度、host、Key、用量（140 个文件） |
| `v10-plugin-v1/` | 第一版 V10 插件（69 个文件） |
| `web-console/` | 第一版 Vue3 控制台（52 个文件） |
| `docs-czapi/` | 旧实现的设计文档 |
| `migrations/0001_init.sql` | 旧库结构 |
| `deploy/`、`scripts/`、`test/` | 旧的部署与测试脚本 |
| `plan.md` | 项目初期的实施计划（含当时的决策记录） |

## 想继续用旧实现？

不建议。现役实现覆盖了旧版的全部能力，并且多了：

- 复用 new-api 的 40+ 上游渠道、模型映射、分组倍率
- 内置计费与用量日志（含 ClickHouse 支持）
- 运维后台 `/admin/`（站点、额度、账本、Key、host、报表）

## 想彻底删掉？

确认不再需要对照参考后，直接删除本目录即可。它不参与任何构建：
`gateway/go.mod` 不引用 `czapi` module，CI 与发布包也不包含它。
