// Package authcontext 负责在认证中间件与各 API 适配器之间传递可信账户身份。
package authcontext

import "context"

type accountIDContextKey struct{}

// WithAccountID 把认证中间件已经校验通过的账户 ID 写入 Context。
// 调用者必须确保该值来自 Token verifier，而不是客户端请求字段。
func WithAccountID(ctx context.Context, accountID string) context.Context {
	return context.WithValue(ctx, accountIDContextKey{}, accountID)
}

// AccountID 读取认证中间件建立的非空账户身份。
func AccountID(ctx context.Context) (string, bool) {
	accountID, ok := ctx.Value(accountIDContextKey{}).(string)
	return accountID, ok && accountID != ""
}
