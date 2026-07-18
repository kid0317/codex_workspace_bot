package observability

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const payloadChunkBytes = 512 * 1024

type AttemptMetadata struct {
	TraceID, Name, SessionID, UserID string
	Input                            any
	Metadata                         map[string]string
}

type ExportConfig struct {
	BaseURL              string
	PublicKey            string
	SecretKey            string
	ExportTimeoutSeconds int
	MaxQueueSize         int
	ProjectID            string
	Environment          string
}

// Recorder owns the OTel provider used for this process. Its hot path only
// creates/ends SDK spans; transport is delegated to the provider's processor.
type Recorder struct {
	provider       *trace.TracerProvider
	tracer         oteltrace.Tracer
	globalMetadata map[string]string
}

type Attempt struct {
	mu             sync.Mutex
	ctx            context.Context
	rootSpan       oteltrace.Span
	span           oteltrace.Span
	tracer         oteltrace.Tracer
	childBase      []attribute.KeyValue
	activeLoop     *protocolLoop
	tools          map[string]oteltrace.Span
	items          map[string]*protocolItem
	lastSnapshot   *Usage
	snapshotSeen   map[usageSnapshotKey]struct{}
	unallocated    Usage
	protocolClosed bool
	lateEventCount int
}

type protocolItem struct {
	id, kind string
	span     oteltrace.Span
	output   strings.Builder
}

type protocolLoop struct {
	itemID       string
	span         oteltrace.Span
	accumulator  LoopUsageAccumulator
	usageInvalid bool
}

func NewWithSpanExporter(exporter trace.SpanExporter) *Recorder {
	return newRecorder(trace.WithSyncer(exporter))
}

// NewOTLP creates the only production exporter used by S08: OTLP/HTTP to the
// project-scoped Langfuse v4 endpoint. Its caller decides whether a failure
// becomes a degraded no-op; no request path waits for this constructor later.
func NewOTLP(ctx context.Context, config ExportConfig) (*Recorder, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" || config.PublicKey == "" || config.SecretKey == "" {
		return nil, fmt.Errorf("langfuse OTLP requires base URL and project credentials")
	}
	if config.ExportTimeoutSeconds <= 0 {
		config.ExportTimeoutSeconds = 2
	}
	if config.MaxQueueSize <= 0 {
		config.MaxQueueSize = 4096
	}
	authorization := "Basic " + base64.StdEncoding.EncodeToString([]byte(config.PublicKey+":"+config.SecretKey))
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpointURL(baseURL+"/api/public/otel/v1/traces"),
		otlptracehttp.WithHeaders(map[string]string{
			"Authorization":                authorization,
			"x-langfuse-ingestion-version": "4",
		}),
		otlptracehttp.WithTimeout(time.Duration(config.ExportTimeoutSeconds)*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("create Langfuse OTLP exporter: %w", err)
	}
	recorder := newRecorder(trace.WithBatcher(exporter,
		trace.WithMaxQueueSize(config.MaxQueueSize),
		trace.WithExportTimeout(time.Duration(config.ExportTimeoutSeconds)*time.Second),
	))
	recorder.globalMetadata = map[string]string{"langfuse_project_id": config.ProjectID, "environment": config.Environment}
	return recorder, nil
}

// NewWithSpanProcessor is useful for deterministic tests and for adapters
// whose exporter construction is owned outside this package.
func NewWithSpanProcessor(processor trace.SpanProcessor) *Recorder {
	return newRecorder(trace.WithSpanProcessor(processor))
}

func newRecorder(option trace.TracerProviderOption) *Recorder {
	provider := trace.NewTracerProvider(
		trace.WithIDGenerator(canonicalTraceIDGenerator{}),
		option,
	)
	return &Recorder{provider: provider, tracer: provider.Tracer("codex-workspace-bot/observability")}
}

func (r *Recorder) Shutdown(ctx context.Context) error {
	if r == nil || r.provider == nil {
		return nil
	}
	return r.provider.Shutdown(ctx)
}

// Start creates the root observation with a caller-supplied canonical 32-hex
// trace ID. The resulting root has no parent; its child observations inherit
// the same context in later runtime integration.
func (r *Recorder) Start(metadata AttemptMetadata) (*Attempt, error) {
	if r == nil || r.tracer == nil {
		return &Attempt{}, nil
	}
	traceID, err := oteltrace.TraceIDFromHex(metadata.TraceID)
	if err != nil || !traceID.IsValid() || metadata.TraceID != strings.ToLower(metadata.TraceID) {
		return nil, fmt.Errorf("invalid canonical trace id %q", metadata.TraceID)
	}
	name := metadata.Name
	if name == "" {
		name = "codex.turn"
	}
	traceAttributes := []attribute.KeyValue{
		attribute.String("langfuse.trace.name", name),
		attribute.String("langfuse.observation.type", "span"),
	}
	if metadata.UserID != "" {
		traceAttributes = append(traceAttributes, attribute.String("langfuse.user.id", metadata.UserID))
	}
	if metadata.SessionID != "" {
		traceAttributes = append(traceAttributes, attribute.String("langfuse.session.id", metadata.SessionID))
	}
	inputChunked := false
	if metadata.Input != nil {
		input, chunked := observationIOValue(metadata.Input)
		inputChunked = chunked
		traceAttributes = append(traceAttributes, attribute.String("langfuse.observation.input", input))
	}
	childBase := make([]attribute.KeyValue, 0, len(metadata.Metadata)+2)
	if metadata.UserID != "" {
		childBase = append(childBase, attribute.String("langfuse.user.id", metadata.UserID))
	}
	if metadata.SessionID != "" {
		childBase = append(childBase, attribute.String("langfuse.session.id", metadata.SessionID))
	}
	for key, value := range r.globalMetadata {
		if value != "" {
			traceAttributes = append(traceAttributes, attribute.String("langfuse.trace.metadata."+key, value))
			childBase = append(childBase, attribute.String("langfuse.trace.metadata."+key, value), attribute.String("langfuse.observation.metadata."+key, value))
		}
	}
	for key, value := range metadata.Metadata {
		traceAttributes = append(traceAttributes, attribute.String("langfuse.trace.metadata."+key, value))
		childBase = append(childBase, attribute.String("langfuse.trace.metadata."+key, value), attribute.String("langfuse.observation.metadata."+key, value))
	}
	ctx := context.WithValue(context.Background(), canonicalTraceIDContextKey{}, traceID)
	rootCtx, rootSpan := r.tracer.Start(ctx, "codex-workspace-bot.turn", oteltrace.WithNewRoot(), oteltrace.WithAttributes(traceAttributes...))
	protocolAttrs := append(append([]attribute.KeyValue{}, childBase...), attribute.String("langfuse.observation.type", "generation"), attribute.String("langfuse.observation.metadata.semantic_kind", "codex_turn"))
	protocolCtx, span := r.tracer.Start(rootCtx, name, oteltrace.WithAttributes(protocolAttrs...))
	attempt := &Attempt{ctx: protocolCtx, rootSpan: rootSpan, span: span, tracer: r.tracer, childBase: childBase}
	if inputChunked {
		attempt.RecordBusinessPayload("root.input", metadata.Input)
	}
	return attempt, nil
}

func (a *Attempt) End(output any, terminalErr error) {
	if a == nil || a.rootSpan == nil {
		return
	}
	if output != nil {
		rendered, chunked := observationIOValue(output)
		if chunked {
			a.RecordBusinessPayload("root.output", output)
		}
		a.rootSpan.SetAttributes(attribute.String("langfuse.observation.output", rendered))
	}
	if terminalErr != nil {
		a.rootSpan.RecordError(terminalErr)
		a.rootSpan.SetStatus(codes.Error, terminalErr.Error())
	}
	a.mu.Lock()
	if !a.protocolClosed {
		a.closeProtocolLocked(nil, terminalErr)
	}
	a.mu.Unlock()
	a.rootSpan.End()
}

func observationIOValue(value any) (string, bool) {
	encoded := jsonValue(value)
	if len(encoded) <= payloadChunkBytes {
		return encoded, false
	}
	return `{"content_chunked":true}`, true
}

// RecordProtocolEvent creates a plaintext, generic child event for every
// routed App Server payload. Known reasoning items additionally define the
// protocol-visible Agent loop used for snapshot-delta attribution. Unknown
// payloads remain events instead of being silently dropped.
func (a *Attempt) RecordProtocolEvent(generation, sequence uint64, method string, payload json.RawMessage) {
	if a == nil || a.span == nil {
		return
	}
	a.mu.Lock()
	if a.protocolClosed {
		a.lateEventCount++
		a.span.SetAttributes(attribute.Int("langfuse.trace.metadata.late_event_count", a.lateEventCount))
		a.mu.Unlock()
		return
	}
	if itemID, itemType := protocolItemIdentity(payload); itemType == "reasoning" && itemID != "" {
		if a.activeLoop == nil || a.activeLoop.itemID != itemID {
			a.closeLoopLocked()
			loopInput, loopChunked := observationIOValue(payload)
			loopCtx, span := a.tracer.Start(a.ctx, "agent.loop", oteltrace.WithAttributes(a.childAttrs(
				attribute.String("langfuse.observation.type", "generation"),
				attribute.String("langfuse.observation.metadata.semantic_kind", "agent_loop"),
				attribute.String("langfuse.observation.input", loopInput),
			)...))
			if loopChunked {
				a.recordBusinessPayloadLocked("reasoning."+itemID+".input", payload)
			}
			a.activeLoop = &protocolLoop{itemID: itemID, span: span}
			if a.lastSnapshot != nil {
				baseline := *a.lastSnapshot
				a.activeLoop.accumulator.baseline = &baseline
			}
			a.ctx = loopCtx
		}
	}
	if method == "thread/tokenUsage/updated" {
		if snapshot, ok := protocolUsage(payload); ok {
			key := usageSnapshotKey{generation: generation, seq: sequence}
			if a.snapshotSeen == nil {
				a.snapshotSeen = make(map[usageSnapshotKey]struct{})
			}
			if _, duplicate := a.snapshotSeen[key]; !duplicate {
				trusted := true
				a.snapshotSeen[key] = struct{}{}
				if a.activeLoop != nil {
					if _, _, err := a.activeLoop.accumulator.Observe(generation, sequence, snapshot); err != nil {
						a.activeLoop.span.SetAttributes(attribute.Bool("langfuse.observation.metadata.usage_available", false), attribute.String("langfuse.observation.metadata.usage_unavailable_reason", "snapshot_regression"))
						a.activeLoop.accumulator.baseline = nil
						a.activeLoop.usageInvalid = true
						a.lastSnapshot = nil
						trusted = false
					}
				} else if a.lastSnapshot != nil {
					if delta, err := snapshot.subtract(*a.lastSnapshot); err == nil {
						a.unallocated.add(delta)
					} else {
						a.span.SetAttributes(attribute.Bool("langfuse.observation.metadata.usage_available", false), attribute.String("langfuse.observation.metadata.usage_unavailable_reason", "snapshot_regression"))
						a.lastSnapshot = nil
						trusted = false
					}
				}
				if trusted {
					last := snapshot
					a.lastSnapshot = &last
				} else {
					a.span.SetAttributes(attribute.Bool("langfuse.observation.metadata.unallocated_usage", true))
				}
			}
		}
	}
	if a.projectItemLocked(method, payload) {
		a.mu.Unlock()
		return
	}
	if len(payload) > payloadChunkBytes {
		a.mu.Unlock()
		a.RecordBusinessPayload("protocol."+strings.ReplaceAll(method, "/", "."), json.RawMessage(payload))
		return
	}
	parent := a.ctx
	tracer := a.tracer
	a.mu.Unlock()
	_, event := tracer.Start(parent, "codex."+strings.ReplaceAll(method, "/", "."), oteltrace.WithAttributes(a.childAttrs(
		attribute.String("langfuse.observation.type", "event"),
		attribute.String("langfuse.observation.input", jsonValue(payload)),
		attribute.String("langfuse.observation.metadata.protocol_method", method),
	)...))
	event.End()
}

// CloseProtocol is idempotent and marks the Turn's App Server terminal. It
// does not close the root request span: channel delivery can still succeed or
// fail afterwards and remains a separate terminal dimension.
func (a *Attempt) CloseProtocol(usage *Usage, terminalErr error) {
	if a == nil || a.span == nil {
		return
	}
	a.mu.Lock()
	if a.protocolClosed {
		a.mu.Unlock()
		return
	}
	a.closeProtocolLocked(usage, terminalErr)
	a.mu.Unlock()
}

func (a *Attempt) closeProtocolLocked(usage *Usage, terminalErr error) {
	a.protocolClosed = true
	a.closeLoopLocked()
	for itemID, item := range a.items {
		item.span.SetAttributes(attribute.Bool("langfuse.observation.metadata.item_terminal", true))
		item.span.End()
		delete(a.items, itemID)
	}
	for callID, tool := range a.tools {
		tool.SetAttributes(attribute.Bool("langfuse.observation.metadata.tool_result_available", false), attribute.String("langfuse.observation.metadata.tool_result_unavailable_reason", "protocol_terminal_before_tool_result"))
		tool.End()
		delete(a.tools, callID)
	}
	if usage != nil {
		a.span.SetAttributes(rawUsageAttributes("langfuse.observation.metadata.", *usage)...)
		if details, ok, reason := usage.UsageDetails(); ok {
			a.span.SetAttributes(attribute.String("langfuse.observation.usage_details", jsonValue(details)))
		} else {
			a.span.SetAttributes(attribute.Bool("langfuse.observation.metadata.usage_details_available", false), attribute.String("langfuse.observation.metadata.usage_details_unavailable_reason", reason))
		}
	}
	if a.unallocated != (Usage{}) {
		a.span.SetAttributes(rawUsageAttributes("langfuse.observation.metadata.unallocated_", a.unallocated)...)
	}
	if terminalErr != nil {
		a.span.SetAttributes(attribute.String("langfuse.observation.metadata.codex_status", "failed"), attribute.String("langfuse.observation.metadata.codex_error_code", terminalErr.Error()))
	} else {
		a.span.SetAttributes(attribute.String("langfuse.observation.metadata.codex_status", "completed"))
	}
	a.span.End()
}

// SetSessionUsage writes the post-transaction conversation total onto the
// still-open request root. It never performs accounting itself.
func (a *Attempt) SetSessionUsage(total SessionUsageTotal) {
	if a == nil || a.span == nil || total.CompletedTurnCount == 0 {
		return
	}
	attributes := rawUsageAttributes("langfuse.trace.metadata.session_usage_", total.Usage)
	attributes = append(attributes, attribute.String("langfuse.trace.metadata.session_completed_turn_count", strconv.FormatInt(total.CompletedTurnCount, 10)))
	a.rootSpan.SetAttributes(attributes...)
}

func (a *Attempt) MarkSessionUsageIncomplete() {
	if a == nil || a.span == nil {
		return
	}
	a.span.SetAttributes(attribute.Bool("langfuse.observation.metadata.session_total_incomplete_for_turn", true))
	a.rootSpan.SetAttributes(attribute.Bool("langfuse.trace.metadata.session_total_incomplete_for_turn", true))
}

func (a *Attempt) MarkSessionUsageSnapshotFallback() {
	if a == nil || a.span == nil { return }
	a.span.SetAttributes(attribute.String("langfuse.observation.metadata.session_usage_source", "thread_token_usage_snapshot"))
	a.rootSpan.SetAttributes(attribute.String("langfuse.trace.metadata.session_usage_source", "thread_token_usage_snapshot"))
}

// RecordBusinessPayload preserves large plaintext content as deterministic,
// reassemblable child events instead of relying on one oversized OTLP
// attribute. It is used for attachment bytes, script streams and oversized
// App Server envelopes.
func (a *Attempt) RecordBusinessPayload(name string, value any) {
	if a == nil || a.span == nil {
		return
	}
	a.mu.Lock()
	if a.protocolClosed {
		a.mu.Unlock()
		return
	}
	a.recordBusinessPayloadLocked(name, value)
	a.mu.Unlock()

}

func (a *Attempt) recordBusinessPayloadLocked(name string, value any) {
	parent, tracer := a.ctx, a.tracer
	attrs := a.childAttrs(attribute.String("langfuse.observation.type", "event"), attribute.String("langfuse.observation.metadata.semantic_kind", name))
	encoded := jsonValue(value)
	chunks := splitUTF8Chunks(encoded, payloadChunkBytes)
	for index, chunk := range chunks {
		chunkAttrs := append(append([]attribute.KeyValue{}, attrs...), attribute.Int("langfuse.observation.metadata.chunk_index", index), attribute.Int("langfuse.observation.metadata.chunk_count", len(chunks)), attribute.String("langfuse.observation.input", chunk))
		_, span := tracer.Start(parent, "payload."+name, oteltrace.WithAttributes(chunkAttrs...))
		span.End()
	}
}

func splitUTF8Chunks(value string, limit int) []string {
	if limit < 1 || len(value) <= limit {
		return []string{value}
	}
	chunks := make([]string, 0, (len(value)+limit-1)/limit)
	for start := 0; start < len(value); {
		end := start + limit
		if end > len(value) {
			end = len(value)
		}
		for end < len(value) && end > start && !utf8.ValidString(value[start:end]) {
			end--
		}
		if end == start {
			_, size := utf8.DecodeRuneInString(value[start:])
			end += size
		}
		chunks = append(chunks, value[start:end])
		start = end
	}
	return chunks
}

// StartTool and EndTool are paired around the actual dynamic-tool handler. The
// business arguments/result remain plaintext after the narrow credential-only
// sanitizer has run.
func (a *Attempt) StartTool(callID, tool string, arguments json.RawMessage) bool {
	if a == nil || a.span == nil || callID == "" {
		return false
	}
	a.mu.Lock()
	if a.protocolClosed {
		a.lateEventCount++
		a.span.SetAttributes(attribute.Int("langfuse.observation.metadata.late_tool_request_after_protocol_terminal", a.lateEventCount))
		a.mu.Unlock()
		return false
	}
	if a.tools == nil {
		a.tools = make(map[string]oteltrace.Span)
	}
	if _, exists := a.tools[callID]; exists {
		a.mu.Unlock()
		return true
	}
	input, chunked := observationIOValue(arguments)
	_, span := a.tracer.Start(a.ctx, "tool."+tool, oteltrace.WithAttributes(a.childAttrs(
		attribute.String("langfuse.observation.type", "span"),
		attribute.String("langfuse.observation.metadata.semantic_kind", "tool"),
		attribute.String("langfuse.observation.metadata.tool_name", tool),
		attribute.String("langfuse.observation.metadata.tool_call_id", callID),
		attribute.String("langfuse.observation.input", input),
	)...))
	a.tools[callID] = span
	a.mu.Unlock()
	if chunked {
		a.RecordBusinessPayload("tool."+tool+".input", arguments)
	}
	return true
}

func (a *Attempt) EndTool(callID string, result any, terminalErr error) {
	if a == nil || callID == "" {
		return
	}
	a.mu.Lock()
	span := a.tools[callID]
	if span != nil {
		delete(a.tools, callID)
	}
	a.mu.Unlock()
	if span == nil {
		return
	}
	if result != nil {
		output, chunked := observationIOValue(result)
		span.SetAttributes(attribute.String("langfuse.observation.output", output))
		if chunked {
			a.RecordBusinessPayload("tool.result", result)
		}
	}
	if terminalErr != nil {
		span.RecordError(terminalErr)
		span.SetStatus(codes.Error, terminalErr.Error())
	}
	span.End()
}

func (a *Attempt) closeLoopLocked() {
	if a.activeLoop == nil {
		return
	}
	total := a.activeLoop.accumulator.Total()
	if a.activeLoop.usageInvalid {
		a.activeLoop.span.SetAttributes(attribute.Bool("langfuse.observation.metadata.usage_details_available", false), attribute.String("langfuse.observation.metadata.usage_details_unavailable_reason", "snapshot_regression"))
	} else if total != (Usage{}) {
		if details, ok, reason := total.UsageDetails(); ok {
			a.activeLoop.span.SetAttributes(attribute.String("langfuse.observation.usage_details", jsonValue(details)))
		} else {
			a.activeLoop.span.SetAttributes(attribute.Bool("langfuse.observation.metadata.usage_details_available", false), attribute.String("langfuse.observation.metadata.usage_details_unavailable_reason", reason))
		}
		a.activeLoop.span.SetAttributes(rawUsageAttributes("langfuse.observation.metadata.loop_", total)...)
	}
	a.activeLoop.span.End()
	a.activeLoop = nil
	a.ctx = oteltrace.ContextWithSpan(context.Background(), a.span)
}

func (a *Attempt) childAttrs(extra ...attribute.KeyValue) []attribute.KeyValue {
	attributes := make([]attribute.KeyValue, 0, len(a.childBase)+len(extra))
	attributes = append(attributes, a.childBase...)
	return append(attributes, extra...)
}

// projectItemLocked folds high-volume delta notifications into their item span
// and ends the span only on item/completed. This preserves the full plaintext
// return while preventing one observation per streaming fragment.
func (a *Attempt) projectItemLocked(method string, payload json.RawMessage) bool {
	itemID, itemType := protocolItemIdentity(payload)
	if itemID == "" {
		var delta struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
			Text   string `json:"text"`
		}
		if json.Unmarshal(payload, &delta) == nil && delta.ItemID != "" && (strings.Contains(method, "/delta") || strings.Contains(method, "/outputDelta")) {
			if item := a.items[delta.ItemID]; item != nil {
				item.output.WriteString(firstNonEmpty(delta.Delta, delta.Text))
				return true
			}
		}
		return false
	}
	if a.items == nil {
		a.items = make(map[string]*protocolItem)
	}
	item := a.items[itemID]
	if method == "item/started" && item == nil {
		name, observationType := itemObservation(itemType)
		input, chunked := observationIOValue(payload)
		_, span := a.tracer.Start(a.ctx, name, oteltrace.WithAttributes(a.childAttrs(
			attribute.String("langfuse.observation.type", observationType),
			attribute.String("langfuse.observation.metadata.item_id", itemID),
			attribute.String("langfuse.observation.metadata.item_type", itemType),
			attribute.String("langfuse.observation.input", input),
		)...))
		if chunked {
			a.recordBusinessPayloadLocked("item."+itemID+".started", payload)
		}
		item = &protocolItem{id: itemID, kind: itemType, span: span}
		a.items[itemID] = item
		return true
	}
	if item == nil && (method == "item/completed" || strings.Contains(method, "/delta") || strings.Contains(method, "/outputDelta")) {
		name, observationType := itemObservation(itemType)
		_, span := a.tracer.Start(a.ctx, name, oteltrace.WithAttributes(a.childAttrs(attribute.String("langfuse.observation.type", observationType), attribute.String("langfuse.observation.metadata.item_id", itemID), attribute.String("langfuse.observation.metadata.item_type", itemType))...))
		item = &protocolItem{id: itemID, kind: itemType, span: span}
		a.items[itemID] = item
	}
	if item == nil {
		return false
	}
	if method == "item/completed" {
		_, _, text := protocolItemText(payload)
		if text != "" && !strings.HasSuffix(item.output.String(), text) {
			if strings.HasPrefix(text, item.output.String()) {
				item.output.Reset()
				item.output.WriteString(text)
			} else {
				item.output.WriteString(text)
			}
		}
		output := item.output.String()
		itemPayload := map[string]any{"payload": json.RawMessage(payload), "aggregated_text": output}
		rendered, chunked := observationIOValue(itemPayload)
		item.span.SetAttributes(attribute.String("langfuse.observation.output", rendered))
		if chunked {
			a.recordBusinessPayloadLocked("item."+itemID+".completed", itemPayload)
		}
		if item.kind == "agentMessage" && output != "" {
			renderedOutput, outputChunked := observationIOValue(output)
			a.span.SetAttributes(attribute.String("langfuse.observation.output", renderedOutput))
			if outputChunked {
				a.recordBusinessPayloadLocked("item."+itemID+".agent_output", output)
			}
		}
		item.span.End()
		delete(a.items, itemID)
		return true
	}
	var delta struct {
		Delta  string `json:"delta"`
		Text   string `json:"text"`
		Output string `json:"output"`
	}
	if json.Unmarshal(payload, &delta) == nil {
		item.output.WriteString(firstNonEmpty(delta.Delta, delta.Text, delta.Output))
	}
	return true
}

func itemObservation(kind string) (string, string) {
	switch kind {
	case "agentMessage":
		return "codex.agent_message", "generation"
	case "commandExecution":
		return "tool.command", "span"
	case "fileChange":
		return "tool.file_change", "span"
	case "mcpToolCall":
		return "tool.mcp", "span"
	case "webSearch", "webFetch":
		return "tool.web", "span"
	default:
		return "codex.item." + kind, "span"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func rawUsageAttributes(prefix string, usage Usage) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String(prefix+"input_tokens", strconv.FormatInt(usage.InputTokens, 10)),
		attribute.String(prefix+"output_tokens", strconv.FormatInt(usage.OutputTokens, 10)),
		attribute.String(prefix+"cached_input_tokens", strconv.FormatInt(usage.CachedInputTokens, 10)),
		attribute.String(prefix+"reasoning_output_tokens", strconv.FormatInt(usage.ReasoningOutputTokens, 10)),
		attribute.String(prefix+"total_tokens", strconv.FormatInt(usage.TotalTokens, 10)),
	}
}

func protocolItemIdentity(payload json.RawMessage) (string, string) {
	var params struct {
		Item struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"item"`
	}
	if json.Unmarshal(payload, &params) != nil {
		return "", ""
	}
	return params.Item.ID, params.Item.Type
}

func protocolItemText(payload json.RawMessage) (string, string, string) {
	var params struct {
		Item struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
	}
	if json.Unmarshal(payload, &params) != nil {
		return "", "", ""
	}
	return params.Item.ID, params.Item.Type, params.Item.Text
}

func protocolUsage(payload json.RawMessage) (Usage, bool) {
	var value any
	if json.Unmarshal(payload, &value) != nil {
		return Usage{}, false
	}
	return findUsage(value)
}

func findUsage(value any) (Usage, bool) {
	switch typed := value.(type) {
	case map[string]any:
		input, inputOK := number(typed["inputTokens"])
		output, outputOK := number(typed["outputTokens"])
		total, totalOK := number(typed["totalTokens"])
		if inputOK && outputOK && totalOK {
			cached, _ := number(typed["cachedInputTokens"])
			reasoning, _ := number(typed["reasoningOutputTokens"])
			return Usage{InputTokens: input, OutputTokens: output, CachedInputTokens: cached, ReasoningOutputTokens: reasoning, TotalTokens: total}, true
		}
		for _, child := range typed {
			if usage, ok := findUsage(child); ok {
				return usage, true
			}
		}
	case []any:
		for _, child := range typed {
			if usage, ok := findUsage(child); ok {
				return usage, true
			}
		}
	}
	return Usage{}, false
}

func number(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), typed == float64(int64(typed))
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func jsonValue(value any) string {
	encoded, err := json.Marshal(SanitizeBusinessValue(value))
	if err != nil {
		return fmt.Sprintf(`{"marshal_error":%q}`, err.Error())
	}
	return string(encoded)
}

type canonicalTraceIDContextKey struct{}

type canonicalTraceIDGenerator struct{}

func (canonicalTraceIDGenerator) NewIDs(ctx context.Context) (oteltrace.TraceID, oteltrace.SpanID) {
	if traceID, ok := ctx.Value(canonicalTraceIDContextKey{}).(oteltrace.TraceID); ok && traceID.IsValid() {
		return traceID, randomSpanID()
	}
	return randomTraceID(), randomSpanID()
}

func (canonicalTraceIDGenerator) NewSpanID(context.Context, oteltrace.TraceID) oteltrace.SpanID {
	return randomSpanID()
}

var fallbackIDCounter atomic.Uint64

func randomTraceID() oteltrace.TraceID {
	var value oteltrace.TraceID
	if _, err := cryptorand.Read(value[:]); err == nil && value.IsValid() {
		return value
	}
	value[15] = byte(fallbackIDCounter.Add(1))
	if !value.IsValid() {
		value[0] = 1
	}
	return value
}

func randomSpanID() oteltrace.SpanID {
	var value oteltrace.SpanID
	if _, err := cryptorand.Read(value[:]); err == nil && value.IsValid() {
		return value
	}
	value[7] = byte(fallbackIDCounter.Add(1))
	if !value.IsValid() {
		value[0] = 1
	}
	return value
}
