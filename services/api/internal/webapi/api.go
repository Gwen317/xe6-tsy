package webapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	recordsv1 "github.com/1024XEngineer/xe6-tsy/packages/contracts/records/v1"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/accounts"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/delivery"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/domain"
	"github.com/1024XEngineer/xe6-tsy/services/api/internal/usage"
)

// API 将带版本的 HTTP 请求适配到账户、用量和消息投递用例。
// 该层只负责协议解析、认证上下文和错误映射，不在 Handler 中实现登录或计费业务规则。
type API struct {
	accounts accounts.Service
	usage    usage.Service
	delivery delivery.Service
	tokens   accounts.AccessTokenVerifier
}

// New 构造账户、用量和消息投递 HTTP 路由。
// 返回的 ServeMux 允许主程序继续挂载 Session、语言和记录模块；所有受保护路由都必须先通过
// AccessTokenVerifier 建立可信账户身份，verifier 缺失时安全返回未授权。
func New(accountsService accounts.Service, usageService usage.Service, deliveryService delivery.Service, tokens accounts.AccessTokenVerifier) *http.ServeMux {
	a := &API{accounts: accountsService, usage: usageService, delivery: deliveryService, tokens: tokens}
	mux := http.NewServeMux()
	// 登录公开入口：创建匿名身份、申请验证码、手机号登录、刷新和退出各自拥有独立契约。
	mux.HandleFunc("POST /api/v1/auth/anonymous", a.createAnonymous)
	mux.HandleFunc("POST /api/v1/auth/verification-codes", a.createPhoneChallenge)
	mux.HandleFunc("POST /api/v1/auth/phone/login", a.verifyPhone)
	mux.HandleFunc("POST /api/v1/auth/token/refresh", a.refresh)
	mux.HandleFunc("POST /api/v1/auth/logout", a.logout)
	// 账户、用量和消息接口只接受认证中间件解析出的 account_id，不相信客户端自行传入的身份字段。
	mux.Handle("GET /api/v1/account/me", a.authenticate(http.HandlerFunc(a.me)))
	mux.Handle("GET /api/v1/voice-sessions/{id}/usage", a.authenticate(http.HandlerFunc(a.sessionUsage)))
	mux.Handle("GET /api/v1/usage/summary", a.authenticate(http.HandlerFunc(a.accountUsage)))
	mux.Handle("POST /api/v1/outbound-messages", a.authenticate(http.HandlerFunc(a.createMessage)))
	mux.Handle("GET /api/v1/outbound-messages/{message_id}", a.authenticate(http.HandlerFunc(a.getMessage)))
	mux.Handle("POST /api/v1/outbound-deliveries/{message_id}/retry", a.authenticate(http.HandlerFunc(a.retryMessage)))
	mux.Handle("GET /api/v1/account/message-preferences", a.authenticate(http.HandlerFunc(a.preferences)))
	mux.Handle("PUT /api/v1/account/message-preferences/{channel}", a.authenticate(http.HandlerFunc(a.putPreference)))
	mux.Handle("GET /api/v1/account/message-targets", a.authenticate(http.HandlerFunc(a.listMessageTargets)))
	mux.Handle("POST /api/v1/account/message-targets/email/verification-codes", a.authenticate(http.HandlerFunc(a.requestEmailBindVerification)))
	mux.Handle("POST /api/v1/account/message-targets/email/bind", a.authenticate(http.HandlerFunc(a.bindEmailTarget)))
	mux.Handle("DELETE /api/v1/account/message-targets/email/{destination_ref}", a.authenticate(http.HandlerFunc(a.unbindEmailTarget)))
	mux.Handle("POST /api/v1/account/message-targets/wechat/bind", a.authenticate(http.HandlerFunc(a.bindWeChatTarget)))
	mux.Handle("DELETE /api/v1/account/message-targets/wechat/{destination_ref}", a.authenticate(http.HandlerFunc(a.unbindWeChatTarget)))
	return mux
}

// authenticate 只接受通过校验的 Bearer Token，并用 verifier 返回的身份覆盖已有账户上下文。
// 覆盖行为可以防止上游或测试代码预先注入伪造 account_id 后绕过认证。
func (a *API) authenticate(next http.Handler) http.Handler {
	return Authenticate(a.tokens, next)
}

// Authenticate 校验 HTTP Bearer Token，并把验证后的账户身份写入请求 Context。
// 语言、Session 和语音记录等其他模块也复用这一中间件，保证所有用户侧受保护路由具有相同认证边界。
func Authenticate(tokens accounts.AccessTokenVerifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if next == nil {
			writeError(w, r, domain.ErrUnauthorized)
			return
		}
		ctx, err := AuthenticatedContext(r.Context(), r.Header.Get("Authorization"), tokens)
		if err != nil {
			writeError(w, r, domain.ErrUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AuthenticatedContext 严格解析一个 Bearer 凭证，并只把 verifier 确认的账户 ID 写入新 Context。
// 它独立于中间件，是为了让“手机号登录时可选合并匿名账户”这种条件认证流程复用同一校验逻辑。
func AuthenticatedContext(ctx context.Context, authorization string, tokens accounts.AccessTokenVerifier) (context.Context, error) {
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || tokens == nil {
		return nil, domain.ErrUnauthorized
	}
	claims, err := tokens.VerifyAccessToken(ctx, parts[1])
	if err != nil || claims.AccountID == "" {
		return nil, domain.ErrUnauthorized
	}
	return WithAccountID(ctx, claims.AccountID), nil
}

// errorResponse is the shared public error envelope defined by the OpenAPI contract.
type errorResponse struct {
	Error struct {
		Code      string         `json:"code"`
		Message   string         `json:"message"`
		RequestID string         `json:"request_id"`
		Retryable bool           `json:"retryable"`
		Details   map[string]any `json:"details"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// writeError maps stable domain errors to the shared HTTP error contract.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := http.StatusInternalServerError, "internal_error"
	switch {
	case errors.Is(err, domain.ErrNotImplemented):
		status, code = http.StatusNotImplemented, "not_implemented"
	case errors.Is(err, domain.ErrInvalidArgument):
		status, code = http.StatusBadRequest, "invalid_argument"
	case errors.Is(err, domain.ErrUnauthorized):
		status, code = http.StatusUnauthorized, "unauthorized"
	case errors.Is(err, domain.ErrForbidden):
		status, code = http.StatusForbidden, "forbidden"
	case errors.Is(err, domain.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, domain.ErrConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, domain.ErrRateLimited):
		status, code = http.StatusTooManyRequests, "rate_limited"
	}
	var response errorResponse
	response.Error.Code = code
	response.Error.Message = code
	response.Error.RequestID = requestID(r)
	response.Error.Details = map[string]any{}
	writeJSON(w, status, response)
}

// requestID preserves an upstream request identifier or creates a non-sensitive fallback.
func requestID(r *http.Request) string {
	if id := r.Header.Get("X-Request-ID"); id != "" {
		return id
	}
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "req_unavailable"
	}
	return "req_" + hex.EncodeToString(bytes)
}

// decodeJSON accepts one bounded JSON value and rejects unknown fields or trailing content.
func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return domain.ErrInvalidArgument
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return domain.ErrInvalidArgument
	}
	return nil
}

// accountID 只读取认证中间件的输出，绝不从请求体、查询参数或 Header 接受客户端账户 ID。
func accountID(r *http.Request) (string, error) {
	id, ok := accountIDFromContext(r.Context())
	if !ok {
		return "", domain.ErrUnauthorized
	}
	return id, nil
}

// createAnonymous 为首次访问客户端建立临时账户和第一组 Token。
// 后续产生的 Session、Turn 和 Usage 都先归属该账户，完成手机号登录后再迁移到正式账户。
func (a *API) createAnonymous(w http.ResponseWriter, r *http.Request) {
	result, err := a.accounts.CreateAnonymous(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// createPhoneChallenge 接收规范化手机号并返回不透明 challenge_id。
// 验证码明文只能由 VerificationSender 交付，不能出现在 HTTP 响应中。
func (a *API) createPhoneChallenge(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Phone string `json:"phone"`
	}
	if decodeJSON(r, &request) != nil || request.Phone == "" {
		writeError(w, r, domain.ErrInvalidArgument)
		return
	}
	id, err := a.accounts.CreatePhoneChallenge(r.Context(), request.Phone)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"challenge_id": id})
}

// verifyPhone 完成验证码登录，并支持把当前客户端拥有的匿名账户合并到手机号账户。
// 不带 anonymous_account_id 时该接口保持公开；一旦要求合并，就必须额外验证该匿名账户的 Access Token。
func (a *API) verifyPhone(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ChallengeID        string `json:"challenge_id"`
		Code               string `json:"code"`
		AnonymousAccountID string `json:"anonymous_account_id,omitempty"`
	}
	if decodeJSON(r, &request) != nil || request.ChallengeID == "" || request.Code == "" {
		writeError(w, r, domain.ErrInvalidArgument)
		return
	}
	ctx := r.Context()
	if request.AnonymousAccountID != "" {
		// 合并会迁移历史会话和业务数据，不能只凭请求体中的匿名账户 ID 执行。
		var err error
		ctx, err = AuthenticatedContext(ctx, r.Header.Get("Authorization"), a.tokens)
		if err != nil {
			writeError(w, r, err)
			return
		}
		accountID, ok := AccountIDFromContext(ctx)
		if !ok || accountID != request.AnonymousAccountID {
			writeError(w, r, domain.ErrForbidden)
			return
		}
	}
	result, err := a.accounts.VerifyPhone(ctx, request.ChallengeID, request.Code, request.AnonymousAccountID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// refresh 用 Refresh Token 原子轮换登录会话，成功后旧 Refresh Token 不能再次使用。
func (a *API) refresh(w http.ResponseWriter, r *http.Request) {
	var request struct {
		RefreshToken string `json:"refresh_token"`
	}
	if decodeJSON(r, &request) != nil || request.RefreshToken == "" {
		writeError(w, r, domain.ErrInvalidArgument)
		return
	}
	result, err := a.accounts.Refresh(r.Context(), request.RefreshToken)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// logout 按 Refresh Token 撤销登录会话；关联 Access Token 的会话状态校验随后也会失败。
func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	var request struct {
		RefreshToken string `json:"refresh_token"`
	}
	if decodeJSON(r, &request) != nil || request.RefreshToken == "" {
		writeError(w, r, domain.ErrInvalidArgument)
		return
	}
	if err := a.accounts.Logout(r.Context(), request.RefreshToken); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// me 返回认证上下文对应的当前账户，不提供查询其他账户的入口。
func (a *API) me(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := a.accounts.Me(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// sessionUsage 查询当前账户名下一场业务 Session 的聚合用量。
// 账户 ID 来自 Token，Session 归属由用量 Service 再次向 Session 权威数据源校验。
func (a *API) sessionUsage(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := a.usage.SessionUsage(r.Context(), id, r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// accountUsage 查询当前账户在半开时间区间 [period_start, period_end) 内的聚合用量。
// HTTP 层先保证时间可解析且 start < end，业务层会再次校验账户和时间边界。
func (a *API) accountUsage(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	start, e1 := time.Parse(time.RFC3339, r.URL.Query().Get("period_start"))
	end, e2 := time.Parse(time.RFC3339, r.URL.Query().Get("period_end"))
	if e1 != nil || e2 != nil || !start.Before(end) {
		writeError(w, r, domain.ErrInvalidArgument)
		return
	}
	result, err := a.usage.AccountUsage(r.Context(), id, start, end)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) createMessage(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var input delivery.CreateInput
	if decodeJSON(r, &input) != nil || !delivery.IsSupportedChannel(input.Channel) || input.DestinationRef == "" || len(input.TurnIDs) == 0 || len(input.TurnIDs) > recordsv1.MaxFinalTurnBatchSize || r.Header.Get("Idempotency-Key") == "" || hasDuplicates(input.TurnIDs) {
		writeError(w, r, domain.ErrInvalidArgument)
		return
	}
	input.AccountID = id
	input.IdempotencyKey = r.Header.Get("Idempotency-Key")
	result, err := a.delivery.Create(r.Context(), input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (a *API) getMessage(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := a.delivery.Get(r.Context(), id, r.PathValue("message_id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) retryMessage(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if key := r.Header.Get("Idempotency-Key"); key == "" || len(key) > delivery.MaxIdempotencyKeyLength {
		writeError(w, r, domain.ErrInvalidArgument)
		return
	}
	result, err := a.delivery.Retry(r.Context(), id, r.PathValue("message_id"), r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (a *API) preferences(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, err := a.delivery.Preferences(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": result})
}

func (a *API) putPreference(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	channel := delivery.Channel(r.PathValue("channel"))
	if !delivery.IsSupportedChannel(channel) {
		writeError(w, r, domain.ErrInvalidArgument)
		return
	}
	var request struct {
		Enabled        *bool  `json:"enabled"`
		DestinationRef string `json:"destination_ref,omitempty"`
	}
	if decodeJSON(r, &request) != nil || request.Enabled == nil {
		writeError(w, r, domain.ErrInvalidArgument)
		return
	}
	var result delivery.Preference
	if request.DestinationRef != "" {
		if service, ok := a.delivery.(delivery.AutomaticPreferenceService); ok {
			result, err = service.PutPreferenceForDestination(r.Context(), id, channel, *request.Enabled, request.DestinationRef)
		} else {
			err = domain.ErrInvalidArgument
		}
	} else {
		result, err = a.delivery.PutPreference(r.Context(), id, channel, *request.Enabled)
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) listMessageTargets(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var channel *delivery.Channel
	if raw := strings.TrimSpace(r.URL.Query().Get("channel")); raw != "" {
		value := delivery.Channel(raw)
		if !delivery.IsSupportedChannel(value) {
			writeError(w, r, domain.ErrInvalidArgument)
			return
		}
		channel = &value
	}
	result, err := a.delivery.ListMessageTargets(r.Context(), id, channel)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": result})
}

func (a *API) requestEmailBindVerification(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request struct {
		Email          string `json:"email"`
		DestinationRef string `json:"destination_ref"`
	}
	if decodeJSON(r, &request) != nil || strings.TrimSpace(request.Email) == "" {
		writeError(w, r, domain.ErrInvalidArgument)
		return
	}
	if err := a.delivery.RequestEmailBindVerification(r.Context(), id, request.Email, request.DestinationRef); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (a *API) bindEmailTarget(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request struct {
		Token string `json:"token"`
	}
	if decodeJSON(r, &request) != nil || strings.TrimSpace(request.Token) == "" {
		writeError(w, r, domain.ErrInvalidArgument)
		return
	}
	result, err := a.delivery.BindEmailTarget(r.Context(), id, request.Token)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) unbindEmailTarget(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	destinationRef := strings.TrimSpace(r.PathValue("destination_ref"))
	if destinationRef == "" {
		writeError(w, r, domain.ErrInvalidArgument)
		return
	}
	if err := a.delivery.RevokeMessageTarget(r.Context(), id, delivery.ChannelEmail, destinationRef); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) bindWeChatTarget(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request struct {
		Code string `json:"code"`
	}
	if decodeJSON(r, &request) != nil || strings.TrimSpace(request.Code) == "" {
		writeError(w, r, domain.ErrInvalidArgument)
		return
	}
	result, err := a.delivery.BindWeChatTarget(r.Context(), id, request.Code)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) unbindWeChatTarget(w http.ResponseWriter, r *http.Request) {
	id, err := accountID(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	destinationRef := strings.TrimSpace(r.PathValue("destination_ref"))
	if destinationRef == "" {
		writeError(w, r, domain.ErrInvalidArgument)
		return
	}
	if err := a.delivery.RevokeMessageTarget(r.Context(), id, delivery.ChannelWeChat, destinationRef); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// hasDuplicates also rejects empty identifiers so Turn selection stays unambiguous.
func hasDuplicates(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return true
		}
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}
