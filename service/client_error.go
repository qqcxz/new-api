package service

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// PublicUpstreamError contains only site-owned fields. Provider error codes,
// parameter names and metadata can identify a provider just like its message.
func PublicUpstreamError(c *gin.Context, statusCode int, codes ...string) types.OpenAIError {
	category := common.UpstreamErrorCategory(statusCode, codes...)
	if category == "api_error" && c != nil && c.Writer != nil && c.Writer.Written() && strings.HasPrefix(c.Writer.Header().Get("Content-Type"), "text/event-stream") {
		category = "stream_interrupted"
	}
	message := ""
	if category == "api_error" {
		message = strings.TrimSpace(operation_setting.GetGeneralSetting().UpstreamErrorMessage)
		if message == "" {
			message = i18n.T(c, i18n.MsgRelayServiceError)
		}
	} else {
		message = i18n.T(c, "relay.error."+category)
	}
	if c != nil && c.GetString(common.RequestIdKey) != "" {
		message = common.MessageWithRequestId(message, c.GetString(common.RequestIdKey))
	}
	return types.OpenAIError{Message: message, Type: "api_error", Code: category}
}

// ClientRelayError projects an error for the client without changing the error
// used by retry, channel health, accounting, metrics or administrator logs.
func ClientRelayError(c *gin.Context, apiErr *types.NewAPIError) *types.NewAPIError {
	if apiErr == nil || !operation_setting.GetGeneralSetting().HideUpstreamError {
		return apiErr
	}
	if common.IsLocalRelayError(string(apiErr.GetErrorType()), string(apiErr.GetErrorCode())) {
		return apiErr
	}
	original := apiErr.ToOpenAIError()
	return types.WithOpenAIError(PublicUpstreamError(c, apiErr.StatusCode, string(apiErr.GetErrorCode()), original.Type), apiErr.StatusCode)
}

// ClientTaskError applies the same output policy to asynchronous submission and
// fetch failures, leaving the original TaskError available for diagnostics.
func ClientTaskError(c *gin.Context, taskErr *taskdto.TaskError) *taskdto.TaskError {
	if taskErr == nil || taskErr.LocalError || !operation_setting.GetGeneralSetting().HideUpstreamError {
		return taskErr
	}
	public := PublicUpstreamError(c, taskErr.StatusCode, taskErr.Code)
	result := *taskErr
	result.Code, result.Message, result.Data = public.Code.(string), public.Message, nil
	return &result
}

// SanitizeUpstreamErrorPayload covers errors received after streaming starts.
// Keep protocol field positions, output, usage and routing identifiers intact;
// inspect only error fields, never generated content or tool results.
func SanitizeUpstreamErrorPayload(c *gin.Context, payload []byte, httpStatus ...int) []byte {
	if !operation_setting.GetGeneralSetting().HideUpstreamError || (c != nil && c.GetBool("client_relay_error_prepared")) {
		return payload
	}
	root := gjson.ParseBytes(payload)
	if !root.IsObject() {
		return payload
	}
	eventType := root.Get("type").String()
	errorField := root.Get("error")
	responseError := root.Get("response.error")
	statusError := root.Get("response.status_details.error")
	hasError := errorField.Exists() && errorField.Type != gjson.Null
	hasResponseError := responseError.Exists() && responseError.Type != gjson.Null
	hasStatusError := statusError.Exists() && statusError.Type != gjson.Null
	if !hasError && !hasResponseError && !hasStatusError && eventType != "error" && eventType != "response.failed" && eventType != "response.error" {
		return payload
	}
	var body map[string]common.RawMessage
	if common.Unmarshal(payload, &body) != nil {
		return payload
	}
	statusCode := 0
	if len(httpStatus) > 0 {
		statusCode = httpStatus[0]
	}
	// A stream may already have HTTP 200 headers. Read only structured error
	// status/code fields; never infer a cause from generated text or messages.
	for _, path := range []string{"error.status_code", "error.status", "error.code", "response.error.status_code", "response.error.status", "response.error.code", "response.status_details.error.status_code", "response.status_details.error.code", "status_code", "status", "code"} {
		if status, err := strconv.Atoi(root.Get(path).String()); err == nil && status >= 400 && status <= 599 {
			if statusCode < 400 || statusCode > 599 {
				statusCode = status
			}
			break
		}
	}
	public := PublicUpstreamError(c, statusCode,
		root.Get("error.code").String(), root.Get("response.error.code").String(), root.Get("response.status_details.error.code").String(), root.Get("code").String(),
		root.Get("error.type").String(), root.Get("response.error.type").String(), root.Get("response.status_details.error.type").String())
	publicError, err := common.Marshal(public)
	if err != nil {
		return payload
	}
	switch {
	case hasError && root.Get("object").String() == "response":
		// Non-streaming failed Responses may still carry delivered output/usage.
		body["error"] = publicError
	case hasError || eventType == "error" || eventType == "response.error":
		filtered := make(map[string]common.RawMessage)
		for _, key := range []string{"type", "event_id", "response_id", "stream_id", "status", "sequence_number"} {
			if value, ok := body[key]; ok {
				filtered[key] = value
			}
		}
		if hasError {
			filtered["error"] = publicError
		} else {
			// Responses SSE errors are flat; WebSocket/OpenAI errors are nested.
			filtered["message"], _ = common.Marshal(public.Message)
			filtered["code"], _ = common.Marshal(public.Code)
			if _, ok := body["param"]; ok {
				filtered["param"] = common.RawMessage(`null`)
			}
		}
		body = filtered
	default:
		var response map[string]common.RawMessage
		if common.Unmarshal(body["response"], &response) != nil || response == nil {
			return payload
		}
		if hasResponseError || eventType == "response.failed" {
			response["error"] = publicError
		}
		if hasStatusError {
			var details map[string]common.RawMessage
			if common.Unmarshal(response["status_details"], &details) == nil {
				details = map[string]common.RawMessage{"type": details["type"], "error": publicError}
				response["status_details"], _ = common.Marshal(details)
			}
		}
		body["response"], _ = common.Marshal(response)
	}
	result, err := common.Marshal(body)
	if err != nil {
		return payload
	}
	if !bytes.Equal(result, payload) {
		logger.LogError(c, common.LogText("relay error: %s", common.LocalLogPreview(common.MaskSensitiveInfo(string(payload), common.UpstreamAddresses(c)...))))
	}
	return result
}
