package service

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

var (
	streamCredentialField = regexp.MustCompile(`(?i)(\b(?:authorization|x-api-key|api[_-]?key|access_token|client_secret)\b["']?\s*[:=]\s*["']?)(?:(?:bearer|basic)\s+)?[^"'\s,;}\]]+`)
	streamBearerToken     = regexp.MustCompile(`(?i)(\bBearer\s+)[A-Za-z0-9._~+/-]+=*`)
	streamProviderKey     = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]+`)
)

// maskStreamDiagnostic bounds provider diagnostics and removes credentials before
// they enter retained stream state or administrator logs.
func maskStreamDiagnostic(ctx *gin.Context, info *relaycommon.RelayInfo, value string) string {
	if info != nil && info.ChannelMeta != nil && info.ApiKey != "" {
		value = strings.ReplaceAll(value, info.ApiKey, "***")
	}
	value = streamCredentialField.ReplaceAllString(value, "${1}***")
	value = streamBearerToken.ReplaceAllString(value, "${1}***")
	value = streamProviderKey.ReplaceAllString(value, "***")
	if ctx != nil {
		value = common.MaskSensitiveInfo(value, common.UpstreamAddresses(ctx)...)
	}
	if len(value) > 4096 {
		return strings.ToValidUTF8(value[:4096], "") + "…"
	}
	return strings.ToValidUTF8(value, "")
}

// ObserveUpstreamStreamFailure reads only protocol error envelopes. Generated
// text, tool results and arbitrary nested "error" members are never classified.
// Call before client-facing error sanitization; raw payloads are not retained.
func ObserveUpstreamStreamFailure(info *relaycommon.RelayInfo, data []byte) {
	if info == nil || info.StreamStatus == nil {
		return
	}
	root := gjson.ParseBytes(data)
	if !root.IsObject() {
		return
	}
	event := root.Get("type").String()
	failure := root.Get("error")
	for _, path := range []string{"response.error", "response.status_details.error"} {
		if nested := root.Get(path); nested.Exists() && nested.Type != gjson.Null {
			failure = nested
			break
		}
	}
	hasError := failure.Exists() && failure.Type != gjson.Null && failure.Raw != "false" && failure.String() != ""
	if !hasError && event != "error" && event != "upstream_error" && event != "response.failed" && event != "response.error" && root.Get("response.status").String() != "failed" {
		return
	}
	code, errorType := failure.Get("code").String(), failure.Get("type").String()
	message := failure.Get("message").String()
	if failure.Type == gjson.String {
		message = failure.String()
	}
	if code == "" {
		code = root.Get("code").String()
	}
	if message == "" {
		message = root.Get("message").String()
	}
	status := 0
	for _, value := range []string{failure.Get("status_code").String(), failure.Get("status").String(), code, root.Get("status_code").String()} {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 400 && parsed <= 599 {
			status = parsed
			break
		}
	}
	info.StreamStatus.MarkFailed(code, errorType, status)
	info.StreamStatus.RecordFailureDetails(event, maskStreamDiagnostic(nil, info, strings.TrimSpace(message)))
}

// shouldWaiveFailedStream is deliberately narrower than a transport error:
// cancellation, missing final usage, or a blank visible answer is not proof
// that no work occurred. Only an explicit failure with no output or reported
// billable usage can discard an input-only local estimate or a fixed fee.
func shouldWaiveFailedStream(info *relaycommon.RelayInfo, usage *dto.Usage, localEstimate bool, summary textQuotaSummary) bool {
	if info == nil || !info.IsStream || !info.StreamStatus.ResponseFailed() {
		return false
	}
	outcome := info.StreamStatus.OutcomeSnapshot()
	if outcome.EndReason == relaycommon.StreamEndReasonClientGone || outcome.EndReason == relaycommon.StreamEndReasonPingFail {
		return false
	}
	facts := info.StreamStatus.AccountingSnapshot()
	if facts.OutputObserved || facts.UpstreamUsageObserved || summary.CompletionTokens > 0 || !summary.ToolCallSurchargeQuota.IsZero() {
		return false
	}
	if info.ResponsesUsageInfo != nil {
		for _, tool := range info.ResponsesUsageInfo.BuiltInTools {
			if tool != nil && tool.CallCount > 0 {
				return false
			}
		}
	}
	if info.ImageRequestCount > 0 {
		return false
	}
	// Native billing snapshots can contain cache or hidden reasoning usage even
	// when converted top-level prompt/completion counts are zero.
	if native, ok := usageFromBillingUsage(usage); ok && dto.HasOpenAIUsageTokens(native) {
		// A purely local native snapshot is still an estimate. Mixed snapshots
		// are protected by UpstreamUsageObserved above, recorded on ingestion.
		if !usage.BillingUsage.Estimated || !facts.UsageEstimated {
			return false
		}
		// Only a plain input estimate may be discarded. Canonical Gemini
		// snapshots synthesize TextTokens, so remove that redundant input field
		// too, while retaining native cache, audio and hidden reasoning work.
		nativeDetails := *native
		nativeDetails.PromptTokens = 0
		nativeDetails.TotalTokens = 0
		nativeDetails.InputTokens = 0
		nativeDetails.PromptTokensDetails.TextTokens = 0
		if dto.HasOpenAIUsageTokens(&nativeDetails) || nativeDetails.Cost != nil || (nativeDetails.OutputTokensDetails != nil && *nativeDetails.OutputTokensDetails != (dto.OutputTokenDetails{})) {
			return false
		}
	}
	if usage != nil {
		if usage.Cost != nil {
			return false
		}
		// An input-only local fallback sets PromptTokens and TotalTokens. Any
		// other usage field can be real cache, audio or hidden reasoning work.
		reportedDetails := *usage
		reportedDetails.PromptTokens = 0
		reportedDetails.TotalTokens = 0
		if dto.HasOpenAIUsageTokens(&reportedDetails) || (usage.OutputTokensDetails != nil && *usage.OutputTokensDetails != (dto.OutputTokenDetails{})) {
			return false
		}
	}
	return localEstimate || facts.UsageEstimated || !dto.HasOpenAIUsageTokens(usage)
}

func streamUsageSource(info *relaycommon.RelayInfo, usage *dto.Usage, localEstimate bool) string {
	facts := info.StreamStatus.AccountingSnapshot()
	estimated := localEstimate || facts.UsageEstimated || (usage != nil && usage.BillingUsage != nil && usage.BillingUsage.Estimated)
	reported := facts.UpstreamUsageObserved
	if !reported && !estimated {
		reported = dto.HasOpenAIUsageTokens(effectiveBillingUsage(usage))
	}
	if reported && estimated {
		return "mixed"
	}
	if reported {
		return "upstream"
	}
	if estimated {
		return "local"
	}
	return "missing"
}

// No upstream billing receipt is available on the relay path. Never infer a
// zero upstream cost from a waiver, HTTP status or the presence/absence of usage.
func appendStreamBillingInfo(other *model.LogOther, info *relaycommon.RelayInfo, usage *dto.Usage, localEstimate bool) {
	if info == nil || !info.IsStream || info.StreamStatus == nil {
		return
	}
	other.SetAdmin("usage_source", streamUsageSource(info, usage, localEstimate))
	outcome := info.StreamStatus.OutcomeSnapshot()
	missingTerminal := outcome.ExpectsTerminal && outcome.Response == relaycommon.ResponseOutcomeUnknown && outcome.EndReason != relaycommon.StreamEndReasonDone
	if info.StreamStatus.ResponseFailed() || !info.StreamStatus.IsNormalEnd() || info.StreamStatus.HasErrors() || missingTerminal {
		other.SetAdmin("upstream_billing_status", "unknown")
	}
}
