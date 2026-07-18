package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRecorderUsesCanonicalTraceIDAndPlaintextBusinessFields(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	recorder := NewWithSpanProcessor(spans)
	t.Cleanup(func() { _ = recorder.Shutdown(context.Background()) })

	attempt, err := recorder.Start(AttemptMetadata{
		TraceID:   "0123456789abcdef0123456789abcdef",
		Name:      "codex.turn",
		SessionID: "app-1:p2p:oc-real",
		UserID:    "ou-real",
		Input:     "user plaintext input",
		Metadata: map[string]string{
			"app_id":       "app-1",
			"chat_id":      "oc-real",
			"user_open_id": "ou-real",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	attempt.End("assistant plaintext output", nil)

	ended := spans.Ended()
	if len(ended) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(ended))
	}
	var span = ended[0]
	for _, candidate := range ended {
		if candidate.Name() == "codex-workspace-bot.turn" {
			span = candidate
			break
		}
	}
	if got := span.SpanContext().TraceID().String(); got != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("trace id = %q", got)
	}
	attributes := span.Attributes()
	if got := attributeString(attributes, "langfuse.user.id"); got != "ou-real" {
		t.Fatalf("user id = %q", got)
	}
	if got := attributeString(attributes, "langfuse.session.id"); got != "app-1:p2p:oc-real" {
		t.Fatalf("session id = %q", got)
	}
	if got := attributeString(attributes, "langfuse.trace.metadata.chat_id"); got != "oc-real" {
		t.Fatalf("chat id = %q", got)
	}
	if got := attributeString(attributes, "langfuse.observation.input"); got != `"user plaintext input"` {
		t.Fatalf("input = %q", got)
	}
	if got := attributeString(attributes, "langfuse.observation.output"); got != `"assistant plaintext output"` {
		t.Fatalf("output = %q", got)
	}
}

func TestNewOTLPUsesLangfuseEndpointAndCredentialHeader(t *testing.T) {
	request := make(chan *http.Request, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, received *http.Request) {
		request <- received.Clone(received.Context())
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	recorder, err := NewOTLP(context.Background(), ExportConfig{BaseURL: server.URL, PublicKey: "pk-test", SecretKey: "sk-test", ExportTimeoutSeconds: 2, MaxQueueSize: 32})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := recorder.Start(AttemptMetadata{TraceID: "0123456789abcdef0123456789abcdef", Input: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	attempt.End("output", nil)
	if err := recorder.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case received := <-request:
		if received.URL.Path != "/api/public/otel/v1/traces" {
			t.Fatalf("path = %q", received.URL.Path)
		}
		if received.Header.Get("x-langfuse-ingestion-version") != "4" {
			t.Fatalf("ingestion version = %q", received.Header.Get("x-langfuse-ingestion-version"))
		}
		username, password, ok := received.BasicAuth()
		if !ok || username != "pk-test" || password != "sk-test" {
			t.Fatal("Basic authentication was not set correctly")
		}
	default:
		t.Fatal("OTLP exporter made no request")
	}
}

func TestAttemptRecordsLoopSnapshotDeltasAndAuthoritativeTurnUsage(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	recorder := NewWithSpanProcessor(spans)
	t.Cleanup(func() { _ = recorder.Shutdown(context.Background()) })
	attempt, err := recorder.Start(AttemptMetadata{TraceID: "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	attempt.RecordProtocolEvent(1, 1, "item/started", []byte(`{"item":{"id":"reason-1","type":"reasoning","text":"reasoning plaintext"}}`))
	attempt.RecordProtocolEvent(1, 2, "thread/tokenUsage/updated", []byte(`{"usage":{"inputTokens":100,"outputTokens":10,"cachedInputTokens":60,"reasoningOutputTokens":2,"totalTokens":110}}`))
	attempt.RecordProtocolEvent(1, 3, "thread/tokenUsage/updated", []byte(`{"usage":{"inputTokens":150,"outputTokens":30,"cachedInputTokens":90,"reasoningOutputTokens":8,"totalTokens":180}}`))
	usage := Usage{InputTokens: 200, OutputTokens: 50, CachedInputTokens: 110, ReasoningOutputTokens: 15, TotalTokens: 250}
	attempt.CloseProtocol(&usage, nil)
	attempt.End("final plaintext", nil)

	var loopFound, rootFound bool
	for _, span := range spans.Ended() {
		switch span.Name() {
		case "agent.loop":
			loopFound = true
			if got := attributeString(span.Attributes(), "langfuse.observation.usage_details"); got == "" || !contains(got, `"total":70`) {
				t.Fatalf("loop usage details = %q", got)
			}
		case "codex.turn":
			rootFound = true
			if got := attributeString(span.Attributes(), "langfuse.observation.metadata.total_tokens"); got != "250" {
				t.Fatalf("root raw total tokens = %q", got)
			}
		}
	}
	if !loopFound || !rootFound {
		t.Fatalf("loopFound=%t rootFound=%t", loopFound, rootFound)
	}
}

func TestAttemptRecordsPlaintextToolInputAndResult(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	recorder := NewWithSpanProcessor(spans)
	t.Cleanup(func() { _ = recorder.Shutdown(context.Background()) })
	attempt, err := recorder.Start(AttemptMetadata{TraceID: "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	attempt.StartTool("call-1", "feishu.doc_read", []byte(`{"document_url":"https://example.test/doc-real"}`))
	attempt.EndTool("call-1", map[string]any{"content": "document plaintext"}, nil)
	attempt.End(nil, nil)
	for _, span := range spans.Ended() {
		if span.Name() != "tool.feishu.doc_read" {
			continue
		}
		if got := attributeString(span.Attributes(), "langfuse.observation.input"); !contains(got, "doc-real") {
			t.Fatalf("tool input = %q", got)
		}
		if got := attributeString(span.Attributes(), "langfuse.observation.output"); !contains(got, "document plaintext") {
			t.Fatalf("tool output = %q", got)
		}
		return
	}
	t.Fatal("tool span not exported")
}

func contains(value, expected string) bool { return strings.Contains(value, expected) }

func TestRecorderRejectsInvalidCanonicalTraceID(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	recorder := NewWithSpanProcessor(spans)
	t.Cleanup(func() { _ = recorder.Shutdown(context.Background()) })
	if _, err := recorder.Start(AttemptMetadata{TraceID: "not-a-trace-id"}); err == nil {
		t.Fatal("Start() error = nil, want invalid trace id")
	}
}

func attributeString(attributes []attribute.KeyValue, key string) string {
	for _, candidate := range attributes {
		if string(candidate.Key) == key {
			return candidate.Value.AsString()
		}
	}
	return ""
}
