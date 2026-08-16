package providerproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestProxyReplacesInboundCredentials(t *testing.T) {
	var gotAuthorization, gotAPIKey, gotPath, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("X-Api-Key")
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer upstream.Close()

	upstreamURL, err := url.Parse(upstream.URL + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(Config{Upstream: upstreamURL, APIKey: "real-provider-key"})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://proxy/responses", strings.NewReader(`{"model":"test"}`))
	req.Header.Set("Authorization", "Bearer attacker-controlled")
	req.Header.Set("X-Api-Key", "attacker-controlled")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	if gotAuthorization != "Bearer real-provider-key" {
		t.Fatalf("Authorization = %q", gotAuthorization)
	}
	if gotAPIKey != "" {
		t.Fatalf("X-Api-Key leaked upstream: %q", gotAPIKey)
	}
	if gotPath != "/v1/responses" {
		t.Fatalf("path = %q, want /v1/responses", gotPath)
	}
	if gotBody != `{"model":"test"}` {
		t.Fatalf("body = %q", gotBody)
	}
}

func TestProxyHealthDoesNotCallUpstream(t *testing.T) {
	upstreamURL, _ := url.Parse("http://127.0.0.1:1/v1")
	handler, err := New(Config{Upstream: upstreamURL, APIKey: "provider-key"})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://proxy/healthz", nil))
	if recorder.Code != http.StatusOK || strings.TrimSpace(recorder.Body.String()) != "ok" {
		t.Fatalf("health response = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestProxyRejectsMissingConfiguration(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New(Config{}) succeeded, want error")
	}
}

