package common

import (
	"net/http"
	"strings"
)

// IsLocalRelayError identifies errors whose site-owned, actionable message must
// survive upstream error privacy. Keep this policy shared by API and log views.
func IsLocalRelayError(errorType, code string) bool {
	if errorType != "new_api_error" || strings.HasPrefix(code, "channel:") {
		return false
	}
	switch code {
	case "do_request_failed", "get_channel_failed", "read_response_body_failed",
		"bad_response_status_code", "bad_response", "bad_response_body",
		"empty_response", "aws_invoke_error", "model_not_found", "prompt_blocked",
		"violation_fee.grok.csam":
		return false
	default:
		return true
	}
}

// UpstreamErrorCategory returns only site-owned categories. Never classify by
// searching a provider's free-form message, or echo unknown codes and metadata.
func UpstreamErrorCategory(statusCode int, codes ...string) string {
	for _, code := range codes {
		switch strings.ToLower(strings.TrimSpace(code)) {
		case "insufficient_quota", "insufficient_balance", "credit_balance_too_low", "billing_hard_limit_reached":
			return "insufficient_quota"
		case "context_length_exceeded", "context_window_exceeded":
			return "context_length_exceeded"
		case "content_policy_violation", "content_filter", "content_filtered", "prompt_blocked", "violation_fee.grok.csam":
			return "content_policy_violation"
		case "model_not_found", "model_not_available":
			return "model_not_found"
		case "channel:invalid_key":
			return "authentication_error"
		}
	}
	// An explicit HTTP error takes precedence over a less specific provider type.
	switch statusCode {
	case http.StatusBadRequest:
		return "invalid_request"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusPaymentRequired:
		return "insufficient_quota"
	case http.StatusForbidden:
		return "access_denied"
	case http.StatusNotFound, http.StatusGone:
		return "not_found"
	case http.StatusMethodNotAllowed:
		return "method_not_allowed"
	case http.StatusRequestTimeout:
		return "request_timeout"
	case http.StatusConflict:
		return "conflict"
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	case http.StatusUnsupportedMediaType:
		return "unsupported_media_type"
	case http.StatusUnprocessableEntity:
		return "unprocessable_entity"
	case http.StatusTooManyRequests:
		return "rate_limit_exceeded"
	case http.StatusInternalServerError:
		return "internal_error"
	case http.StatusNotImplemented:
		return "not_implemented"
	case http.StatusBadGateway:
		return "bad_gateway"
	case http.StatusServiceUnavailable, 529:
		return "service_unavailable"
	case http.StatusGatewayTimeout, 524:
		return "gateway_timeout"
	}
	// Streaming errors often carry a code/type but no HTTP error status.
	for _, code := range codes {
		code = strings.ToLower(strings.TrimSpace(code))
		switch code {
		case "invalid_request", "invalid_request_error", "invalid_argument":
			return "invalid_request"
		case "authentication_error", "invalid_api_key", "unauthenticated":
			return "authentication_error"
		case "access_denied", "permission_error", "permission_denied":
			return "access_denied"
		case "not_found", "not_found_error", "method_not_allowed", "request_timeout", "conflict", "payload_too_large", "unsupported_media_type", "unprocessable_entity", "internal_error", "not_implemented", "bad_gateway", "service_unavailable", "gateway_timeout":
			if code == "not_found_error" {
				return "not_found"
			}
			return code
		case "stream_interrupted", "stream_error":
			return "stream_interrupted"
		case "rate_limit_exceeded", "rate_limit_error", "rate_limit_reached", "resource_exhausted":
			return "rate_limit_exceeded"
		case "overloaded_error", "server_overloaded":
			return "service_unavailable"
		case "server_error":
			return "internal_error"
		case "timeout", "timeout_error":
			return "gateway_timeout"
		}
	}
	return "api_error"
}
