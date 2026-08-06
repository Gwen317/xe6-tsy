package accounts

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
)

// LogVerificationSender 仅用于本地开发，把一次性验证码写入结构化日志。
// 手机号会被掩码处理，避免开发日志直接泄露完整号码。
type LogVerificationSender struct{}

func (LogVerificationSender) SendCode(_ context.Context, phone, code string) error {
	slog.Info("phone verification code sent", "phone", maskPhone(phone), "code", code)
	return nil
}

// MemoryVerificationSender 按手机号保存最近一次验证码，供测试在不连接短信供应商时走完整登录链路。
// 可选 fallback 仍会收到同一验证码，便于同时观察交付行为。
type MemoryVerificationSender struct {
	mu     sync.Mutex
	codes  map[string]string
	sender VerificationSender
}

func NewMemoryVerificationSender(fallback VerificationSender) *MemoryVerificationSender {
	return &MemoryVerificationSender{codes: make(map[string]string), sender: fallback}
}

func (m *MemoryVerificationSender) SendCode(ctx context.Context, phone, code string) error {
	m.mu.Lock()
	m.codes[phone] = code
	m.mu.Unlock()
	if m.sender != nil {
		return m.sender.SendCode(ctx, phone, code)
	}
	return nil
}

func (m *MemoryVerificationSender) LastCode(phone string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	code, ok := m.codes[phone]
	return code, ok
}

// VerificationSenderFromEnv 根据环境配置选择验证码交付适配器。
// 空值或 log 只允许本地结构化日志实现；未知生产适配器返回 nil，使登录功能安全关闭。
func VerificationSenderFromEnv() VerificationSender {
	switch os.Getenv("VERIFICATION_SENDER") {
	case "", "log":
		return LogVerificationSender{}
	default:
		return nil
	}
}

func maskPhone(phone string) string {
	if len(phone) <= 4 {
		return "****"
	}
	return fmt.Sprintf("****%s", phone[len(phone)-4:])
}
