package controller

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientRelayErrorPrivacy(t *testing.T) {
	require.NoError(t, i18n.Init())
	settings := operation_setting.GetGeneralSetting()
	previous := *settings
	t.Cleanup(func() { *settings = previous })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("Accept-Language", "en")
	c.Set(common.RequestIdKey, "local-request")
	original := types.WithOpenAIError(types.OpenAIError{
		Message: "PrivateProvider account out of credits", Type: "PrivateProvider_error",
		Param: "PrivateProvider_account", Code: "invalid_request",
		Metadata: common.RawMessage(`{"provider":"PrivateProvider"}`),
	}, http.StatusTooManyRequests)
	originalMessage := original.Error()
	decision := service.DecideRelayRetry(c, original, 1)

	settings.HideUpstreamError = false
	assert.Same(t, original, service.ClientRelayError(c, original))
	settings.HideUpstreamError, settings.UpstreamErrorMessage = true, " Please contact support "
	public := service.ClientRelayError(c, original)
	assert.Equal(t, original.StatusCode, public.StatusCode)
	encoded, err := common.Marshal(public.ToOpenAIError())
	require.NoError(t, err)
	assert.JSONEq(t, `{"message":"The request rate limit was reached. Please try again later. (request id: local-request)","type":"api_error","param":"","code":"rate_limit_exceeded"}`, string(encoded))
	assert.Equal(t, "rate_limit_exceeded", public.ToClaudeError().Type)
	assert.Equal(t, originalMessage, original.Error(), "admin diagnostics must retain the original error")
	assert.Equal(t, decision, service.DecideRelayRetry(c, original, 1), "privacy must not change retry decisions")

	for _, code := range []types.ErrorCode{types.ErrorCodeInvalidRequest, types.ErrorCodeAccessDenied, types.ErrorCodeInsufficientUserQuota, types.ErrorCodeCountTokenFailed} {
		local := types.NewError(errors.New("actionable local error"), code)
		assert.Same(t, local, service.ClientRelayError(c, local))
	}
	transport := types.NewError(errors.New("dial PrivateProvider.example: connection refused"), types.ErrorCodeDoRequestFailed)
	assert.NotContains(t, service.ClientRelayError(c, transport).Error(), "PrivateProvider")
	assert.Nil(t, service.ClientRelayError(c, nil))

	settings.UpstreamErrorMessage = " \n "
	assert.Equal(t, "The request rate limit was reached. Please try again later. (request id: local-request)", service.ClientRelayError(c, original).Error())
	c.Request.Header.Set("Accept-Language", "zh-CN")
	assert.Contains(t, service.ClientRelayError(c, original).Error(), "请求过于频繁")
	unknown := types.WithOpenAIError(types.OpenAIError{Message: "PrivateProvider failed", Code: "private_code"}, 599)
	assert.Equal(t, "服务暂时无法完成请求，请稍后重试。 (request id: local-request)", service.ClientRelayError(c, unknown).Error())
	settings.UpstreamErrorMessage = " Please contact support "
	assert.Equal(t, "Please contact support (request id: local-request)", service.ClientRelayError(c, unknown).Error())
}

func TestTaskErrorPrivacy(t *testing.T) {
	require.NoError(t, i18n.Init())
	settings := operation_setting.GetGeneralSetting()
	previous := *settings
	t.Cleanup(func() { *settings = previous })
	settings.HideUpstreamError, settings.UpstreamErrorMessage = true, "Please retry"
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	c.Request.Header.Set("Accept-Language", "en")
	original := &taskdto.TaskError{Code: "PrivateProvider_error", Message: "PrivateProvider failed", StatusCode: http.StatusTooManyRequests, Data: map[string]any{"provider": "PrivateProvider"}}
	respondTaskError(c, original)
	assert.Equal(t, http.StatusTooManyRequests, recorder.Code)
	assert.JSONEq(t, `{"code":"rate_limit_exceeded","message":"The request rate limit was reached. Please try again later.","data":null}`, recorder.Body.String())
	assert.Equal(t, "PrivateProvider failed", original.Message)
	assert.NotNil(t, original.Data)
	original.LocalError = true
	assert.Same(t, original, service.ClientTaskError(c, original))
	original.LocalError = false
	settings.HideUpstreamError = false
	assert.Same(t, original, service.ClientTaskError(c, original))
}

func TestUpstreamErrorPayloadPrivacy(t *testing.T) {
	require.NoError(t, i18n.Init())
	settings := operation_setting.GetGeneralSetting()
	previous := *settings
	t.Cleanup(func() { *settings = previous })
	settings.HideUpstreamError, settings.UpstreamErrorMessage = true, "Service unavailable"
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "local-request")
	for _, tc := range []struct {
		name, payload string
	}{
		{"OpenAI", `{"error":{"message":"PrivateProvider failed","type":"PrivateProvider_error","code":"PrivateProvider_code","metadata":{"provider":"PrivateProvider"}},"provider":"PrivateProvider"}`},
		{"Claude", `{"type":"error","error":{"type":"PrivateProvider_error","message":"PrivateProvider failed"}}`},
		{"bare websocket error", `{"type":"error","event_id":"event-1","stream_id":"stream-1","status":429,"message":"PrivateProvider failed","code":"PrivateProvider_code"}`},
		{"Responses failure", `{"type":"response.failed","sequence_number":3,"response":{"id":"resp-1","status":"failed","error":{"message":"PrivateProvider failed","code":"PrivateProvider_code"},"usage":{"input_tokens":7},"output":[]}}`},
		{"Responses flat error", `{"type":"error","sequence_number":3,"code":"PrivateProvider_code","message":"PrivateProvider failed","param":"PrivateProvider_param"}`},
		{"Responses full failure", `{"object":"response","id":"resp-1","status":"failed","error":{"message":"PrivateProvider failed","code":"PrivateProvider_code"},"usage":{"input_tokens":7},"output":[{"text":"Delivered text"}],"created_at":9007199254740993}`},
		{"Realtime failure", `{"type":"response.done","response":{"id":"resp-1","status":"failed","status_details":{"type":"failed","error":{"message":"PrivateProvider failed"}},"usage":{"input_tokens":7}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := service.SanitizeUpstreamErrorPayload(c, []byte(tc.payload))
			assert.NotContains(t, string(result), "PrivateProvider")
			if tc.name == "bare websocket error" {
				assert.Contains(t, string(result), "rate_limit_exceeded")
				assert.NotContains(t, string(result), "Service unavailable")
			} else {
				assert.Contains(t, string(result), "Service unavailable (request id: local-request)")
			}
			assert.Equal(t, string(result), string(service.SanitizeUpstreamErrorPayload(c, result)), "repeated rendering must be idempotent")
			if strings.Contains(tc.payload, `"usage"`) {
				assert.Contains(t, string(result), `"input_tokens":7`)
			}
			if tc.name == "Responses flat error" {
				var event map[string]any
				require.NoError(t, common.Unmarshal(result, &event))
				assert.Equal(t, "Service unavailable (request id: local-request)", event["message"])
				assert.Equal(t, "api_error", event["code"])
				assert.NotContains(t, event, "error")
			}
			if tc.name == "Responses full failure" {
				assert.Contains(t, string(result), `"object":"response"`)
				assert.Contains(t, string(result), `"id":"resp-1"`)
				assert.Contains(t, string(result), `"output":[{"text":"Delivered text"}]`)
				assert.Contains(t, string(result), `"created_at":9007199254740993`)
			}
		})
	}
	for _, payload := range []string{
		`{"type":"response.completed","response":{"id":"resp-1","error":null,"output":[{"text":"PrivateProvider"}],"usage":{"input_tokens":7}}}`,
		`{"choices":[{"delta":{"content":"an error occurred at PrivateProvider"}}]}`,
		`[DONE]`,
	} {
		assert.Equal(t, payload, string(service.SanitizeUpstreamErrorPayload(c, []byte(payload))))
	}
	settings.HideUpstreamError = false
	raw := `{"error":{"message":"PrivateProvider original"}}`
	assert.Equal(t, raw, string(service.SanitizeUpstreamErrorPayload(c, []byte(raw))))
}

func TestRelayErrorPrivacyAtClientWriters(t *testing.T) {
	require.NoError(t, i18n.Init())
	settings := operation_setting.GetGeneralSetting()
	previous := *settings
	t.Cleanup(func() { *settings = previous })
	settings.HideUpstreamError, settings.UpstreamErrorMessage = true, "Please retry"
	for _, format := range []types.RelayFormat{types.RelayFormatOpenAI, types.RelayFormatClaude} {
		t.Run(string(format), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/stream", nil)
			c.Set(common.RequestIdKey, "local-request")
			helper.SetEventStreamHeaders(c)
			require.NoError(t, helper.PingData(c))
			apiErr := types.WithOpenAIError(types.OpenAIError{Message: "PrivateProvider failed", Code: "private_code"}, http.StatusTooManyRequests)
			writeRelayStreamError(c, format, apiErr)
			assert.NotContains(t, recorder.Body.String(), "PrivateProvider")
			assert.Contains(t, recorder.Body.String(), "rate_limit_exceeded")
			assert.NotContains(t, recorder.Body.String(), "Please retry")
			assert.Equal(t, http.StatusOK, recorder.Code)
			assert.Contains(t, apiErr.Error(), "PrivateProvider")
			status, ok := common.GetContextKeyType[*relaycommon.StreamStatus](c, constant.ContextKeyResponseStreamStatus)
			require.True(t, ok)
			assert.Equal(t, "private_code", status.OutcomeSnapshot().ErrorCode)
		})
	}
	t.Run("local stream validation remains actionable", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/stream", nil)
		writeRelayStreamError(c, types.RelayFormatClaude, types.NewError(errors.New("max_tokens is required"), types.ErrorCodeInvalidRequest))
		assert.Contains(t, recorder.Body.String(), "max_tokens is required")
	})
	t.Run("HTTP body keeps status and correct content length", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
		service.IOCopyBytesGracefully(c, &http.Response{StatusCode: http.StatusBadGateway, Header: http.Header{"Content-Type": []string{"application/json"}, "X-Provider": []string{"PrivateProvider"}}}, []byte(`{"error":{"message":"PrivateProvider failed"}}`))
		assert.Equal(t, http.StatusBadGateway, recorder.Code)
		assert.Contains(t, recorder.Body.String(), `"code":"bad_gateway"`)
		assert.NotContains(t, recorder.Body.String(), "Please retry")
		assert.NotContains(t, recorder.Body.String(), "PrivateProvider")
		assert.Equal(t, int64(recorder.Body.Len()), recorder.Result().ContentLength)
		assert.Empty(t, recorder.Header().Get("X-Provider"))
	})
	t.Run("raw SSE error", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/stream", nil)
		require.NoError(t, helper.StringData(c, `{"error":{"message":"PrivateProvider failed"}}`))
		assert.NotContains(t, recorder.Body.String(), "PrivateProvider")
		assert.Contains(t, recorder.Body.String(), "Please retry")
	})
	t.Run("raw websocket event", func(t *testing.T) {
		engine := gin.New()
		engine.GET("/ws", func(c *gin.Context) {
			ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
			if err != nil {
				return
			}
			defer ws.Close()
			_ = helper.WssString(c, ws, `{"type":"error","error":{"message":"PrivateProvider failed","code":"private_code"}}`)
		})
		server := httptest.NewServer(engine)
		defer server.Close()
		ws, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/ws", nil)
		require.NoError(t, err)
		defer ws.Close()
		defer response.Body.Close()
		_, payload, err := ws.ReadMessage()
		require.NoError(t, err)
		assert.NotContains(t, string(payload), "PrivateProvider")
		assert.Contains(t, string(payload), "Please retry")
	})
}

func TestRelayPrivacyPreservesLocalHTTPValidation(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previous := *settings
	t.Cleanup(func() { *settings = previous })
	settings.HideUpstreamError, settings.UpstreamErrorMessage = true, "Please retry"
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`not-json`))
	c.Request.Header.Set("Content-Type", "application/json")
	Relay(c, types.RelayFormatOpenAI)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	body, err := io.ReadAll(recorder.Result().Body)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "Please retry")
	assert.Contains(t, string(body), "error")
}

func TestClientRelayErrorPrivacyPreservesErrorCauses(t *testing.T) {
	require.NoError(t, i18n.Init())
	settings := operation_setting.GetGeneralSetting()
	previous := *settings
	t.Cleanup(func() { *settings = previous })
	settings.HideUpstreamError, settings.UpstreamErrorMessage = true, "Unrecognized failure"
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("Accept-Language", "zh-CN")
	c.Set(common.RequestIdKey, "local-request")
	for _, tc := range []struct {
		name, providerCode, code, message string
		status                            int
	}{
		{name: "bad request", status: 400, code: "invalid_request", message: "请求无效，请检查请求格式和参数。"},
		{name: "service authentication", status: 401, code: "authentication_error", message: "服务认证失败，请联系管理员。"},
		{name: "payment required", status: 402, code: "insufficient_quota", message: "服务余额或额度不足，请联系管理员处理。"},
		{name: "forbidden", status: 403, code: "access_denied", message: "请求被拒绝，无权访问所请求的资源。"},
		{name: "not found", status: 404, code: "not_found", message: "请求的资源不存在或已不可用，请检查模型名称或请求地址。"},
		{name: "method", status: 405, code: "method_not_allowed", message: "不支持此请求方法，请检查接口调用方式。"},
		{name: "request timeout", status: 408, code: "request_timeout", message: "请求超时，请稍后重试。"},
		{name: "conflict", status: 409, code: "conflict", message: "请求与当前资源状态冲突，请检查后重试。"},
		{name: "gone", status: 410, code: "not_found", message: "请求的资源不存在或已不可用，请检查模型名称或请求地址。"},
		{name: "body size", status: 413, code: "payload_too_large", message: "请求内容过大，请减少输入内容或文件大小。"},
		{name: "media type", status: 415, code: "unsupported_media_type", message: "不支持此内容类型，请检查 Content-Type 和文件格式。"},
		{name: "validation", status: 422, code: "unprocessable_entity", message: "请求参数无法处理，请检查参数值和格式。"},
		{name: "rate limit", status: 429, code: "rate_limit_exceeded", message: "请求过于频繁，已触发限流，请稍后重试。"},
		{name: "internal error", status: 500, code: "internal_error", message: "服务内部错误，请稍后重试。"},
		{name: "unsupported feature", status: 501, code: "not_implemented", message: "服务暂不支持此功能。"},
		{name: "bad gateway", status: 502, code: "bad_gateway", message: "服务响应异常，请稍后重试。"},
		{name: "unavailable", status: 503, code: "service_unavailable", message: "服务暂时不可用或负载过高，请稍后重试。"},
		{name: "gateway timeout", status: 504, code: "gateway_timeout", message: "服务响应超时，请稍后重试。"},
		{name: "proxy timeout", status: 524, code: "gateway_timeout", message: "服务响应超时，请稍后重试。"},
		{name: "overloaded", status: 529, code: "service_unavailable", message: "服务暂时不可用或负载过高，请稍后重试。"},
		{name: "quota is not rate limiting", status: 429, providerCode: "insufficient_quota", code: "insufficient_quota", message: "服务余额或额度不足，请联系管理员处理。"},
		{name: "context length", status: 400, providerCode: "context_length_exceeded", code: "context_length_exceeded", message: "输入内容超出模型上下文长度限制，请缩短输入或减少输出长度。"},
		{name: "content policy", status: 403, providerCode: "content_policy_violation", code: "content_policy_violation", message: "请求内容被安全策略拒绝，请调整内容后重试。"},
		{name: "model missing", status: 404, providerCode: "model_not_found", code: "model_not_found", message: "请求的模型不存在或暂不可用，请检查模型名称。"},
		{name: "unknown", status: 599, code: "api_error", message: "Unrecognized failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := types.WithOpenAIError(types.OpenAIError{
				Message: "PrivateProvider failed: personal merchant pool https://private.example/account",
				Type:    "PrivateProvider_error", Code: tc.providerCode, Param: "PrivateProvider_account",
				Metadata: common.RawMessage(`{"provider":"PrivateProvider"}`),
			}, tc.status)
			result := service.ClientRelayError(c, original)
			assert.Equal(t, tc.status, result.StatusCode)
			public := result.ToOpenAIError()
			assert.Equal(t, tc.message+" (request id: local-request)", public.Message)
			assert.Equal(t, tc.code, public.Code)
			assert.Empty(t, public.Param)
			assert.Empty(t, public.Metadata)
			assert.Contains(t, original.Error(), "PrivateProvider")
			assert.Equal(t, public, service.ClientRelayError(c, result).ToOpenAIError(), "rendering again must preserve the cause and request ID")
		})
	}
	for _, tc := range []struct {
		code    types.ErrorCode
		status  int
		message string
	}{
		{types.ErrorCodeAccessDenied, 401, "Invalid site token"},
		{types.ErrorCodeInsufficientUserQuota, 402, "Your site account balance is insufficient"},
		{types.ErrorCodeAccessDenied, 403, "No permission for this model"},
		{types.ErrorCodeInvalidRequest, 422, "max_tokens must be positive"},
		{types.ErrorCodeInvalidRequest, 429, "Your site request limit was reached"},
	} {
		local := types.NewErrorWithStatusCode(errors.New(tc.message), tc.code, tc.status)
		assert.Same(t, local, service.ClientRelayError(c, local))
	}
}

func TestUpstreamErrorPrivacyPreservesStreamingCauses(t *testing.T) {
	require.NoError(t, i18n.Init())
	settings := operation_setting.GetGeneralSetting()
	previous := *settings
	t.Cleanup(func() { *settings = previous })
	settings.HideUpstreamError, settings.UpstreamErrorMessage = true, "Unrecognized failure"
	for _, tc := range []struct {
		name, payload, code, message string
	}{
		{"numeric status", `{"type":"error","error":{"status":402,"message":"PrivateProvider credits exhausted"}}`, "insufficient_quota", "服务余额或额度不足"},
		{"numeric code", `{"error":{"code":422,"message":"PrivateProvider validation failed"}}`, "unprocessable_entity", "请求参数无法处理"},
		{"quota code in HTTP 200", `{"type":"response.failed","response":{"id":"resp-1","error":{"code":"insufficient_quota","message":"PrivateProvider credits exhausted"},"output":[{"text":"Delivered text"}],"usage":{"input_tokens":7}}}`, "insufficient_quota", "服务余额或额度不足"},
		{"flat timeout", `{"type":"error","code":"timeout_error","message":"PrivateProvider timed out"}`, "gateway_timeout", "服务响应超时"},
		{"nested realtime type", `{"type":"response.done","response":{"status":"failed","status_details":{"type":"failed","error":{"type":"rate_limit_error","message":"PrivateProvider limited"}}}}`, "rate_limit_exceeded", "请求过于频繁"},
		{"stream interruption", `{"error":{"message":"本轮运行失败智能路由流式响应中途失败。原因: 个人池商家在流式响应开始后中断，当前响应无法再切换商家。解决方案：请重新发起请求。"}}`, "stream_interrupted", "流式响应未能完成，请重新发起请求。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/stream", nil)
			c.Request.Header.Set("Accept-Language", "zh-CN")
			c.Set(common.RequestIdKey, "local-request")
			helper.SetEventStreamHeaders(c)
			require.NoError(t, helper.PingData(c))
			result := service.SanitizeUpstreamErrorPayload(c, []byte(tc.payload))
			assert.Contains(t, string(result), tc.code)
			assert.Contains(t, string(result), tc.message)
			assert.Contains(t, string(result), "request id: local-request")
			assert.NotContains(t, string(result), "PrivateProvider")
			assert.NotContains(t, string(result), "个人池商家")
			assert.NotContains(t, string(result), "Unrecognized failure")
			assert.Equal(t, string(result), string(service.SanitizeUpstreamErrorPayload(c, result)))
			require.NoError(t, helper.StringData(c, string(result)))
			assert.Equal(t, http.StatusOK, recorder.Code)
			if tc.name == "quota code in HTTP 200" {
				assert.Contains(t, string(result), "Delivered text")
				assert.Contains(t, string(result), `"input_tokens":7`)
			}
		})
	}
}
