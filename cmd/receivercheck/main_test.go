package main

import (
	"strings"
	"testing"
)

func TestCheckReceiversRequiresExactConnectedSet(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		expected []string
		wantErr  bool
	}{
		{name: "exact", payload: `{"receivers":{"a":{"state":"connected"},"b":{"state":"connected"}},"observability":"ready"}`, expected: []string{"a", "b"}},
		{name: "unrelated state ignored", payload: `{"receivers":{"a":{"state":"connected"}},"other":{"state":"failed"}}`, expected: []string{"a"}},
		{name: "wrong same count", payload: `{"receivers":{"wrong":{"state":"connected"}}}`, expected: []string{"right"}, wantErr: true},
		{name: "mixed", payload: `{"receivers":{"a":{"state":"connected"},"b":{"state":"reconnecting"}}}`, expected: []string{"a", "b"}, wantErr: true},
		{name: "too few", payload: `{"receivers":{"a":{"state":"connected"}}}`, expected: []string{"a", "b"}, wantErr: true},
		{name: "too many", payload: `{"receivers":{"a":{"state":"connected"},"b":{"state":"connected"}}}`, expected: []string{"a"}, wantErr: true},
		{name: "empty", payload: `{"receivers":{}}`, expected: []string{"a"}, wantErr: true},
		{name: "malformed", payload: `{"receivers":`, expected: []string{"a"}, wantErr: true},
		{name: "trailing garbage", payload: `{"receivers":{"a":{"state":"connected"}}}garbage`, expected: []string{"a"}, wantErr: true},
		{name: "missing receivers", payload: `{"state":"connected"}`, expected: []string{"a"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkReceivers(strings.NewReader(tc.payload), tc.expected)
			if tc.wantErr && err == nil {
				t.Fatal("checkReceivers() error = nil, want failure")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("checkReceivers() error = %v", err)
			}
		})
	}
}

func TestCheckReceiversRejectsNonPositiveExpectedCount(t *testing.T) {
	if err := checkReceivers(strings.NewReader(`{"receivers":{}}`), nil); err == nil {
		t.Fatal("checkReceivers() error = nil, want positive-count validation")
	}
}

func TestReadLocalBaseURLUsesConfiguredLoopbackPort(t *testing.T) {
	for _, tc := range []struct {
		config string
		want   string
	}{
		{config: "server:\n  listen_addr: 127.0.0.1:9191\n", want: "http://127.0.0.1:9191"},
		{config: "server:\n  listen_addr: localhost:8088\n", want: "http://localhost:8088"},
		{config: "server:\n  listen_addr: '[::1]:7070'\n", want: "http://[::1]:7070"},
	} {
		got, err := readLocalBaseURL(strings.NewReader(tc.config))
		if err != nil || got != tc.want {
			t.Fatalf("readLocalBaseURL() = %q, %v; want %q", got, err, tc.want)
		}
	}
}

func TestReadLocalBaseURLRejectsNonLoopbackOrInvalidPort(t *testing.T) {
	for _, config := range []string{
		"server:\n  listen_addr: 0.0.0.0:8080\n",
		"server:\n  listen_addr: 192.168.1.2:8080\n",
		"server:\n  listen_addr: 127.0.0.1:0\n",
		"server:\n  listen_addr: 127.0.0.1:99999\n",
	} {
		if _, err := readLocalBaseURL(strings.NewReader(config)); err == nil {
			t.Fatalf("readLocalBaseURL(%q) error = nil, want rejection", config)
		}
	}
}
