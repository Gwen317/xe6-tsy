# services/api

Go 应用控制服务，负责业务会话、语言配置、数据访问和状态快照，不是管理后台，也不承载 WebRTC 连接。

## 职责

- 会话创建/结束
- 编排实时会话启动和停止
- 可选语言列表和语言对配置
- 演示客户端/设备接入
- 校验会话归属并签发短期实时连接票据
- 会话状态快照查询
- 健康检查
- 必要的调试记录
- 匿名账户、手机号登录和 Token 生命周期边界
- 会话与账户用量查询边界
- final Turn 的异步消息投递边界

## 非职责

- 不处理实时音频流
- 不交换 SDP offer/answer 或 ICE candidate
- 不创建和保存 PeerConnection、DataChannel、Audio Track
- 不直接调用 ASR/翻译/TTS
- 不维护播放状态机
- 不做组织权限、订单、套餐、支付、发票、术语库和管理后台
- 不在实时主链路中调用第三方消息 Provider

## 建议包结构

```text
services/api/
├── main.go
├── config/
├── devices/
├── sessions/
├── languages/                 # 语言配置：HTTP + Service + Postgres（需 DATABASE_URL）
├── realtimeaccess/            # 会话鉴权和短期实时连接票据
├── internal/
│   ├── accounts/
│   ├── usage/
│   ├── delivery/
│   ├── domain/
│   └── webapi/
├── health/
└── webapi/
```

语言配置能力与本地接线说明见 [`languages/README.md`](./languages/README.md)。

WebRTC config、offer/answer 和 ICE candidate 由 `services/realtime-audio/webrtc`
统一处理。部署时可以由 API Gateway 转发 `/realtime/v1`，但本服务不实现信令逻辑。

## Container process modes

The same binary can run as one combined local process or as separate deployed
roles. Set `LINGOW_PROCESS` to `api` for HTTP only, `worker` for the delivery
dispatcher and consumer, or `migrate` for the one-shot PostgreSQL migration
job. Leaving it unset preserves the local combined API/worker behavior.

The production image is built with `services/api/Dockerfile`; the dedicated
application Compose file is `infra/member5-app.compose.yaml`.

结束会话时，本服务先幂等调用 realtime 的 `Stop`。realtime 确认 Pipeline 和 WebRTC 连接已关闭后，
本服务再把业务会话标记为 `ended`。调用失败时保持会话未结束并重试，不允许只改业务状态而遗留实时连接。

账户、用量和消息投递已提供 PostgreSQL/Valkey 的最小可运行实现。受保护路由只接受经过
`AccessTokenVerifier` 验证后写入 Context 的账户身份，客户端提供的 `X-Account-ID` 不参与
鉴权。账户支持匿名创建、Bearer access token、refresh rotation 和 logout；用量按
`idempotency_key` 幂等落库并提供账户/会话汇总；消息通过 final Turn 快照写入 PostgreSQL
transactional outbox，再由 Valkey Stream worker 异步消费并记录发送状态和重试。

部署时如果 `LINGOW_DELIVERY_PROVIDER=unconfigured`，worker 会将发送尝试标记为失败，
不会伪造发送成功。真实邮件 Provider、目的地 provisioning，以及实时转译模块发布
`usage.recorded` 的生产入口仍需由对应模块补齐。

手机号验证码默认关闭（`LINGOW_SMS_PROVIDER=disabled`）。本地联调可设置
`LINGOW_APP_ENV=development`、`LINGOW_SMS_PROVIDER=mock-webhook` 和
`LINGOW_SMS_WEBHOOK_URL`，服务会向该地址发送 `POST {"phone","code"}`；这不是
生产短信 Provider，生产环境会拒绝启用。持久化运行必须配置至少 32 字节且独立于
`LINGOW_TOKEN_SECRET` 的 `LINGOW_AUTH_PEPPER`，用于 HMAC 保护手机号与验证码摘要。

## 语音记录存储集成测试

语音记录 migration 使用 PostgreSQL，并通过 `integration` build tag 与默认离线测试隔离。创建
名称以 `_test` 结尾的专用本地数据库后，设置其连接地址并执行：

```powershell
docker compose -f ../../infra/docker-compose.yml exec postgres createdb -U postgres lingow_records_test
$env:RECORDSTORE_TEST_DATABASE_URL = 'postgres://postgres:postgres@localhost:5432/lingow_records_test?sslmode=disable'
go test -count=1 -tags=integration ./recordstore/...
```

测试 helper 会为每个测试创建并删除随机 schema，拒绝连接名称不以 `_test` 结尾的数据库，且绝不使用
`DATABASE_URL`。
