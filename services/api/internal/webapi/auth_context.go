package webapi

import (
	"context"

	"github.com/1024XEngineer/xe6-tsy/services/api/authcontext"
)

// WithAccountID 是可信认证中间件向 HTTP Handler 交接账户身份的唯一入口。
// Handler 不得从客户端提交的 account_id 推导资源归属。
func WithAccountID(ctx context.Context, accountID string) context.Context {
	return authcontext.WithAccountID(ctx, accountID)
}

// AccountIDFromContext 返回认证中间件已经写入的非空账户 ID。
func AccountIDFromContext(ctx context.Context) (string, bool) {
	return authcontext.AccountID(ctx)
}

// accountIDFromContext 保留原有包内调用入口，统一委托给公开的可信 Context 读取函数。
func accountIDFromContext(ctx context.Context) (string, bool) {
	return AccountIDFromContext(ctx)
}
