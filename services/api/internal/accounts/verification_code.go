package accounts

import (
	"fmt"
	"os"
	"regexp"
)

var normalizedVerificationCodePattern = regexp.MustCompile(`^[0-9]{6}$`)

// VerificationPolicy 控制手机号一次性验证码的生成和接受策略。
// UniversalCode 只用于本地演示或测试；真实发送器启用时必须保持为空并使用加密随机验证码。
type VerificationPolicy struct {
	// UniversalCode 非空时替代随机验证码，并可用于每个新挑战。
	UniversalCode string
}

func (p VerificationPolicy) enabled() bool {
	return p.UniversalCode != ""
}

// VerificationPolicyFromEnv 仅在日志验证码发送器启用时加载固定验证码。
// 其他发送器返回空策略，避免生产短信链路意外接受通用验证码。
func VerificationPolicyFromEnv() (VerificationPolicy, error) {
	switch os.Getenv("VERIFICATION_SENDER") {
	case "", "log":
		code := os.Getenv("VERIFICATION_UNIVERSAL_CODE")
		if code == "" {
			code = "8888"
		}
		normalized := NormalizeVerificationCode(code)
		if !normalizedVerificationCodePattern.MatchString(normalized) {
			return VerificationPolicy{}, fmt.Errorf(
				"VERIFICATION_UNIVERSAL_CODE must normalize to six digits, got %q",
				code,
			)
		}
		return VerificationPolicy{UniversalCode: normalized}, nil
	default:
		return VerificationPolicy{}, nil
	}
}

// NormalizeVerificationCode 将本地演示常用的 8888 简写映射为 API 统一的六位格式。
func NormalizeVerificationCode(code string) string {
	if code == "8888" {
		return "888888"
	}
	return code
}
