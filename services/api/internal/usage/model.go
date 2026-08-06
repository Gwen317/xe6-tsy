package usage

import "time"

// UsageEventVersion 是当前用量模块唯一接受的 usage.recorded 事件版本。
// 生产者升级字段语义时必须发布新版本，消费者不能静默猜测未知版本。
const UsageEventVersion = 1

// Stage 标识某条用量事实由实时处理管线中的哪个 Provider 阶段产生。
type Stage string

const (
	// StageASR 表示语音识别产生的 Token 或音频时长用量。
	StageASR Stage = "asr"
	// StageTranslation 表示文本翻译产生的 Token 用量。
	StageTranslation Stage = "translation"
	// StageTTS 表示语音合成产生的 Token 或音频时长用量。
	StageTTS Stage = "tts"
	// StageDiarization 表示说话人分离产生的用量。
	StageDiarization Stage = "diarization"
)

// RecordInput 是 realtime-audio 提交给账户用量模块的版本化 usage.recorded 事实。
// 它是已经发生的不可变事实，不是客户端可修改的计费请求；重试必须保持幂等键和完整 payload 不变。
type RecordInput struct {
	EventVersion    int       `json:"event_version"`     // 事件契约版本，当前必须为 UsageEventVersion。
	ID              string    `json:"id"`                // 事件唯一 ID，用于追踪生产和消费链路。
	TraceID         string    `json:"trace_id"`          // 跨实时服务、消息流和 API 的关联标识。
	IdempotencyKey  string    `json:"idempotency_key"`   // 业务去重键；同键不同 payload 必须判定冲突。
	AccountID       string    `json:"account_id"`        // 生产者声明的账户，落库前会与 Session 权威归属核对。
	SessionID       string    `json:"session_id"`        // 产生用量的业务 Session。
	TurnID          string    `json:"turn_id"`           // 产生用量的句级 Turn，便于追溯一次实时处理。
	ServiceType     Stage     `json:"service_type"`      // ASR、翻译、TTS 或说话人分离阶段。
	Provider        string    `json:"provider"`          // 实际执行能力的供应商标识。
	Model           string    `json:"model"`             // 供应商模型标识，用于定价和审计。
	InputTokens     int64     `json:"input_tokens"`      // 文本或模型输入 Token 数，不能为负数。
	OutputTokens    int64     `json:"output_tokens"`     // 模型输出 Token 数，不能为负数。
	AudioDurationMS int64     `json:"audio_duration_ms"` // 参与处理的音频时长，单位毫秒。
	CostAmount      string    `json:"cost_amount"`       // 十进制定价金额字符串；空值表示供应商未报告价格。
	Currency        string    `json:"currency"`          // ISO 三位大写币种；必须与 CostAmount 同时为空或非空。
	OccurredAt      time.Time `json:"occurred_at"`       // 用量实际发生时间，汇总按此时间而不是消费时间统计。
}

// Detail 是已持久化的不可变用量明细，并补充服务端实际记录时间。
type Detail struct {
	RecordInput
	RecordedAt time.Time `json:"recorded_at"` // API 成功落库时间，可用于观察消息积压延迟。
}

// StageTotal 汇总同一 Provider 阶段的可计量维度。
// 不同币种不能合并为一条 StageTotal，部分记录缺价时也不能把已知部分伪装成完整总价。
type StageTotal struct {
	ServiceType     Stage  `json:"service_type"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	AudioDurationMS int64  `json:"audio_duration_ms"`
	CostAmount      string `json:"cost_amount"`
	Currency        string `json:"currency"`
}

// Summary 返回单个 Session 或账户时间区间内的用量汇总。
type Summary struct {
	AccountID   string       `json:"account_id"`           // 当前认证账户。
	SessionID   string       `json:"session_id,omitempty"` // Session 查询时返回；账户区间查询时为空。
	PeriodStart time.Time    `json:"period_start"`         // 半开统计区间起点。
	PeriodEnd   time.Time    `json:"period_end"`           // 半开统计区间终点，不包含该时刻。
	Totals      []StageTotal `json:"totals"`               // 按处理阶段汇总后的稳定顺序结果。
}
