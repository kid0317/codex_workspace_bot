package observability

import "testing"

func TestBusinessPayloadIsAlwaysPlaintext(t *testing.T) {
	input := map[string]any{
		"chat_id":       "oc-real",
		"user_input":    "please inspect document https://example.test/doc",
		"authorization": "business authorization text",
		"nested": map[string]any{
			"access_token": "secret-token",
			"token":        "document-capability-token",
			"secret":       "business-secret-label",
			"document_id":  "doc-real",
		},
		"attachment_token": "file-token-plain",
		"url":              "https://example.test/doc?access_token=runtime-token&document_token=doc-token",
		"headers":          map[string]any{"Authorization": "Bearer runtime", "X-Document-Token": "doc-token"},
		"env":              map[string]any{"LANGFUSE_SECRET_KEY": "runtime", "DOCUMENT_TOKEN": "doc-token"},
	}
	got := SanitizeBusinessValue(input).(map[string]any)
	if got["chat_id"] != "oc-real" || got["user_input"] == "" {
		t.Fatalf("business plaintext was changed: %#v", got)
	}
	if got["authorization"] != "business authorization text" {
		t.Fatalf("authorization = %#v", got["authorization"])
	}
	nested := got["nested"].(map[string]any)
	if nested["access_token"] != "secret-token" || nested["document_id"] != "doc-real" || nested["token"] != "document-capability-token" || nested["secret"] != "business-secret-label" {
		t.Fatalf("nested = %#v", nested)
	}
	if got["attachment_token"] != "file-token-plain" {
		t.Fatalf("attachment token was changed: %#v", got["attachment_token"])
	}
	if gotURL := got["url"].(string); gotURL != "https://example.test/doc?access_token=runtime-token&document_token=doc-token" {
		t.Fatalf("url = %q", gotURL)
	}
	if headers := got["headers"].(map[string]any); headers["Authorization"] != "Bearer runtime" || headers["X-Document-Token"] != "doc-token" {
		t.Fatalf("headers = %#v", headers)
	}
	if env := got["env"].(map[string]any); env["LANGFUSE_SECRET_KEY"] != "runtime" || env["DOCUMENT_TOKEN"] != "doc-token" {
		t.Fatalf("env = %#v", env)
	}
}
