# xe6-tsy API 占位骨架

当前阶段只迁移工作人员身份与权限、知识库配置相关的架构边界，不实现具体业务功能。

已包含：

- Go + Gin API 服务启动与优雅退出；
- `GET /healthz` 健康检查；
- `/api/v1` 版本分组和模块注册边界；
- 身份、访问会话、权限判断的方法签名；
- 知识导入、人工复核、发布、读取和退役的方法签名；
- 对应 HTTP 路由与 OpenAPI 占位契约；
- 占位 API 统一返回 `501 Not Implemented`。

本阶段明确不包含：

- 账号密码校验、单点登录、Token 或访问会话实现；
- RBAC/ABAC 权限策略和机构作用域判断；
- 知识文件解析、数据持久化、向量化或 RAG 检索；
- PostgreSQL、Redis、MQ 或第三方供应商接入；
- Mock 账号、演示知识数据或其他可被误认为正式实现的 Fake 业务逻辑。

## 本地命令

需要 Go 1.26 或更高版本：

```bash
make check
make run
```

默认监听 `127.0.0.1:8080`，可通过 `XE6_API_ADDRESS` 和 `XE6_GIN_MODE` 覆盖。

共享 API 契约位于 `packages/contracts/openapi`，工程规则见
[`docs/项目前后端统一开发规范.md`](docs/项目前后端统一开发规范.md)。所有尚未实现的能力、
代码入口和完成门禁集中记录在 [`docs/占位实现清单.md`](docs/占位实现清单.md)。
