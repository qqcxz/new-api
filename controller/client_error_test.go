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
	assert.JSONEq(t, `{"message":"Please contact support (request id: local-request)","type":"api_error","param":"","code":"api_error"}`, string(encoded))
	assert.Equal(t, "api_error", public.ToClaudeError().Type)
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
	assert.Equal(t, "The service could not complete the request. Please try again later. (request id: local-request)", service.ClientRelayError(c, original).Error())
	c.Request.Header.Set("Accept-Language", "zh-CN")
	assert.Contains(t, service.ClientRelayError(c, original).Error(), "服务暂时无法完成请求")
}

func TestTaskErrorPrivacy(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previous := *settings
	t.Cleanup(func() { *settings = previous })
	settings.HideUpstreamError, settings.UpstreamErrorMessage = true, "Please retry"
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/videos", nil)
	original := &taskdto.TaskError{Code: "PrivateProvider_error", Message: "PrivateProvider failed", StatusCode: http.StatusTooManyRequests, Data: map[string]any{"provider": "PrivateProvider"}}
	respondTaskError(c, original)
	assert.Equal(t, http.StatusTooManyRequests, recorder.Code)
	assert.JSONEq(t, `{"code":"api_error","message":"Please retry","data":null}`, recorder.Body.String())
	assert.Equal(t, "PrivateProvider failed", original.Message)
	assert.NotNil(t, original.Data)
	original.LocalError = true
	assert.Same(t, original, service.ClientTaskError(c, original))
	original.LocalError = false
	settings.HideUpstreamError = false
	assert.Same(t, original, service.ClientTaskError(c, original))
}

func TestUpstreamErrorPayloadPrivacy(t *testing.T) {
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
			assert.Contains(t, string(result), "Service unavailable (request id: local-request)")
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
			assert.Contains(t, recorder.Body.String(), "Please retry")
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
