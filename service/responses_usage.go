package service

import (
	"fmt"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/tokenkit"
	"github.com/tidwall/gjson"
)

// ResponsesUsageAccumulator owns the accounting facts for one Responses stream.
// HTTP SSE and WebSocket transports feed the same events into it, then settle
// Finish's usage through the normal text billing path, including interrupted
// streams. Observe and Finish must be called by the same stream owner.
type ResponsesUsageAccumulator struct {
	info           *relaycommon.RelayInfo
	usage          *dto.Usage
	outputText     strings.Builder
	imageCounter   relaycommon.ImageGenerationCallCounter
	imageCommitted bool
	started        bool
	finished       bool
}

func NewResponsesUsageAccumulator(info *relaycommon.RelayInfo) *ResponsesUsageAccumulator {
	if info.StreamStatus == nil {
		info.StreamStatus = relaycommon.NewStreamStatus()
	}
	return &ResponsesUsageAccumulator{info: info, usage: &dto.Usage{}}
}

// Observe feeds one decoded stream event. raw is the same event's wire bytes;
// the vendor tool-usage reader runs on it once, at the terminal event.
func (a *ResponsesUsageAccumulator) Observe(event *dto.ResponsesStreamResponse, raw []byte) {
	if a == nil || event == nil || a.finished {
		return
	}
	ObserveUpstreamStreamFailure(a.info, raw)
	if event.Response != nil {
		a.info.ObserveResponseModel(event.Response.Model)
	}
	a.started = true
	ObserveResponsesOutcome(a.info, event)
	switch event.Type {
	case "response.completed", "response.done", "response.failed", "response.error", "response.incomplete", "response.cancelled", "response.canceled":
		if event.Response != nil {
			ApplyResponsesUsage(a.usage, event.Response.Usage)
			reported := dto.HasOpenAIUsageTokens(a.usage) || (event.Response.Usage != nil && event.Response.Usage.Cost != nil)
			a.info.StreamStatus.ObserveAccounting(false, reported, false)
			if a.outputText.Len() == 0 {
				// Some upstreams carry the output only on the terminal event.
				a.outputText.WriteString(relayconvert.ExtractOutputTextFromResponses(event.Response))
			}
		}
		// Vendor counts are cumulative on the terminal event and replace the
		// web_search_call items counted from output_item.done.
		a.info.ApplyVendorToolUsage(raw)
		if a.imageCommitted {
			return
		}
		// Images that completed before any terminal, failed ones included,
		// were delivered and stay billable; Observe still skips unfinished
		// image items.
		if event.Response != nil {
			for i := range event.Response.Output {
				output := &event.Response.Output[i]
				if responsesOutputHasGeneratedContent(output) {
					a.info.StreamStatus.ObserveAccounting(true, false, false)
				}
				a.imageCounter.Observe(output, &i)
			}
		}
		a.imageCounter.Commit(a.info)
		a.imageCommitted = true
	case "response.output_text.delta", "response.function_call_arguments.delta",
		"response.reasoning_summary_text.delta", "response.reasoning_text.delta", "response.refusal.delta":
		// Retain whitespace for accurate estimates once real output arrives,
		// but a blank placeholder alone must not defeat a failure waiver.
		a.outputText.WriteString(event.Delta)
		a.info.StreamStatus.ObserveAccounting(strings.TrimSpace(event.Delta) != "", false, false)
	case "response.custom_tool_call_input.delta":
		a.outputText.WriteString(event.Delta)
		a.info.StreamStatus.ObserveAccounting(strings.TrimSpace(event.Delta) != "", false, false)
	case "response.audio.delta", "response.output_audio.delta":
		// Audio bytes prove generated work, but are not text token estimates.
		a.info.StreamStatus.ObserveAccounting(event.Delta != "", false, false)
	case "response.image_generation_call.partial_image":
		a.info.StreamStatus.ObserveAccounting(gjson.GetBytes(raw, "partial_image_b64").String() != "", false, false)
	case dto.ResponsesOutputTypeItemAdded, dto.ResponsesOutputTypeItemDone:
		if event.Item == nil {
			return
		}
		if responsesOutputHasGeneratedContent(event.Item) {
			// Tool and reasoning items are work even when they contain no text.
			a.info.StreamStatus.ObserveAccounting(true, false, false)
		}
		if event.Type != dto.ResponsesOutputTypeItemDone {
			return
		}
		switch event.Item.Type {
		case dto.BuildInCallWebSearchCall, dto.BuildInCallFileSearchCall, dto.BuildInCallFunctionCall:
			a.info.CountBillableToolCall(event.Item.Type, event.Item.Name)
		case dto.ResponsesOutputTypeImageGenerationCall:
			if !a.imageCommitted {
				a.imageCounter.Observe(event.Item, event.OutputIndex)
			}
		}
	}
}

func responsesOutputHasGeneratedContent(output *dto.ResponsesOutput) bool {
	if output.Type != "message" {
		return true
	}
	return slices.ContainsFunc(output.Content, func(part dto.ResponsesOutputContent) bool {
		return part.Type != "output_text" || strings.TrimSpace(part.Text) != ""
	})
}

func (a *ResponsesUsageAccumulator) Finish() *dto.Usage {
	if a.finished {
		return a.usage
	}
	a.finished = true
	// A final image item can already have reached the client before the stream
	// disconnects, so completed tool usage is retained even without a terminal.
	if !a.imageCommitted {
		a.imageCounter.Commit(a.info)
		a.imageCommitted = true
	}
	if a.usage.CompletionTokens == 0 {
		if output := a.outputText.String(); output != "" &&
			(!a.info.StreamStatus.ResponseFailed() || strings.TrimSpace(output) != "") {
			a.usage.CompletionTokens = tokenkit.Count(a.info.GetUpstreamModelName(), output)
			a.info.StreamStatus.ObserveAccounting(true, false, true)
		}
	}
	// Retain the historical missing-usage policy for unknown interruptions and
	// cancellations. An explicit failure cannot turn metadata into input usage.
	billsPrompt := a.usage.CompletionTokens != 0 || (a.started && !a.info.StreamStatus.ResponseFailed())
	if a.usage.PromptTokens == 0 && billsPrompt {
		a.usage.PromptTokens = a.info.GetEstimatePromptTokens()
		a.info.StreamStatus.ObserveAccounting(false, false, a.usage.PromptTokens > 0)
	}
	a.usage.TotalTokens = a.usage.PromptTokens + a.usage.CompletionTokens
	if a.usage.BillingUsage != nil {
		a.usage.BillingUsage = dto.CloneBillingUsageWithEstimatedCompletion(a.usage.BillingUsage, a.usage.CompletionTokens)
	}
	return a.usage
}

// ObserveResponsesOutcome records protocol facts and bounded administrator
// diagnostics. Error messages stay separate from health classification.
func ObserveResponsesOutcome(info *relaycommon.RelayInfo, event *dto.ResponsesStreamResponse) {
	if info == nil || info.StreamStatus == nil || event == nil {
		return
	}
	var responseStatus string
	if event.Response != nil {
		_ = common.Unmarshal(event.Response.Status, &responseStatus)
	}
	switch {
	case event.Type == "error" || event.Type == "response.failed" || event.Type == "response.error" || responseStatus == "failed":
		code, errorType, message := event.Code, "", event.Message
		if event.Response != nil {
			if oaiErr := event.Response.GetOpenAIError(); oaiErr != nil {
				if oaiErr.Code != nil {
					code = fmt.Sprint(oaiErr.Code)
				}
				errorType = oaiErr.Type
				message = oaiErr.Message
			}
		}
		info.StreamStatus.MarkFailed(code, errorType, 0)
		info.StreamStatus.RecordFailureDetails(event.Type, maskStreamDiagnostic(nil, info, message))
	case event.Type == "response.incomplete" || responseStatus == "incomplete":
		reason := ""
		if event.Response != nil && event.Response.IncompleteDetails != nil {
			reason = event.Response.IncompleteDetails.Reason
		}
		info.StreamStatus.MarkIncomplete(reason)
	case event.Type == "response.cancelled" || event.Type == "response.canceled" || responseStatus == "cancelled":
		info.StreamStatus.MarkCancelled()
	case event.Type == "response.completed" || event.Type == "response.done" || responseStatus == "completed":
		info.StreamStatus.MarkCompleted()
	}
}

func ApplyResponsesUsage(dst *dto.Usage, src *dto.Usage) {
	if dst == nil || src == nil {
		return
	}
	incoming := relayconvert.NormalizeResponsesUsage(src)
	if src.InputTokensDetails != nil {
		inputDetails := src.InputTokensDetails.Clone()
		incoming.InputTokensDetails = &inputDetails
	}
	if src.OutputTokensDetails != nil {
		incoming.CompletionTokenDetails = *src.OutputTokensDetails
	}
	incoming.PromptCacheHitTokens = src.PromptCacheHitTokens
	dto.MergeUsageNonZero(dst, incoming)
	outputDetails := dst.CompletionTokenDetails
	if outputDetails != (dto.OutputTokenDetails{}) {
		dst.OutputTokensDetails = &outputDetails
	}
}
