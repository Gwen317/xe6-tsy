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
├── languages/
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

WebRTC config、offer/answer 和 ICE candidate 由 `services/realtime-audio/webrtc`
统一处理。部署时可以由 API Gateway 转发 `/realtime/v1`，但本服务不实现信令逻辑。

结束会话时，本服务先幂等调用 realtime 的 `Stop`。realtime 确认 Pipeline 和 WebRTC 连接已关闭后，
本服务再把业务会话标记为 `ended`。调用失败时保持会话未结束并重试，不允许只改业务状态而遗留实时连接。

账户、用量和消息投递提供可编译契约和可注入的业务编排：

- Access Token 支持 HS256 签发、验签和认证 Context 注入；
- Usage Service 校验事件版本、计量字段和 Session 归属，再调用幂等 Repository；
- Delivery Service 校验渠道偏好、目标和 final Turn，原子创建 Message、Attempt 与 Outbox；
- Delivery Worker 通过原子领取/完成边界处理 Ack、Nack 和最多三次指数退避重试。

`main.go` 尚未装配数据库 Repository、跨模块 Reader、验证码发送、Queue 或 Email
Provider。缺少这些生产依赖时，对应业务方法必须继续返回 `not_implemented`，不得伪造成功结果。
Email Provider 仍为占位边界，不执行真实邮件发送。
