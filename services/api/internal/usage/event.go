package usage

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
)

// ParseRecordInput 将消息流中的 usage.recorded v1 JSON 解码为强类型 RecordInput。
// 先解码到原始字符串 Stage，再显式转换和统一校验，未知事件版本或阶段不会被静默接受。
func ParseRecordInput(payload []byte) (RecordInput, error) {
	var raw struct {
		EventVersion    int       `json:"event_version"`
		ID              string    `json:"id"`
		TraceID         string    `json:"trace_id"`
		IdempotencyKey  string    `json:"idempotency_key"`
		AccountID       string    `json:"account_id"`
		SessionID       string    `json:"session_id"`
		TurnID          string    `json:"turn_id"`
		ServiceType     string    `json:"service_type"`
		Provider        string    `json:"provider"`
		Model           string    `json:"model"`
		InputTokens     int64     `json:"input_tokens"`
		OutputTokens    int64     `json:"output_tokens"`
		AudioDurationMS int64     `json:"audio_duration_ms"`
		CostAmount      string    `json:"cost_amount"`
		Currency        string    `json:"currency"`
		OccurredAt      time.Time `json:"occurred_at"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return RecordInput{}, fmt.Errorf("%w: decode usage.recorded payload", domain.ErrInvalidArgument)
	}
	input := RecordInput{
		EventVersion:    raw.EventVersion,
		ID:              raw.ID,
		TraceID:         raw.TraceID,
		IdempotencyKey:  raw.IdempotencyKey,
		AccountID:       raw.AccountID,
		SessionID:       raw.SessionID,
		TurnID:          raw.TurnID,
		ServiceType:     Stage(raw.ServiceType),
		Provider:        raw.Provider,
		Model:           raw.Model,
		InputTokens:     raw.InputTokens,
		OutputTokens:    raw.OutputTokens,
		AudioDurationMS: raw.AudioDurationMS,
		CostAmount:      raw.CostAmount,
		Currency:        raw.Currency,
		OccurredAt:      raw.OccurredAt,
	}
	if err := validate(input); err != nil {
		return RecordInput{}, err
	}
	return input, nil
}

// MarshalRecordInput 在发布前复用同一校验规则，把 RecordInput 编码为 usage.recorded v1 JSON。
// 生产者和消费者共享校验入口，可以降低两侧对字段含义理解不一致的风险。
func MarshalRecordInput(input RecordInput) ([]byte, error) {
	if err := validate(input); err != nil {
		return nil, err
	}
	return json.Marshal(input)
}
