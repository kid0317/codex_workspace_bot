package main

import (
	"strings"
	"testing"
)

func TestCheckReceiversRequiresExactConnectedSet(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		expected int
		wantErr  bool
	}{
		{name: "exact", payload: `{"receivers":{"a":{"state":"connected"},"b":{"state":"connected"}},"observability":"ready"}`, expected: 2},
		{name: "unrelated state ignored", payload: `{"receivers":{"a":{"state":"connected"}},"other":{"state":"failed"}}`, expected: 1},
		{name: "mixed", payload: `{"receivers":{"a":{"state":"connected"},"b":{"state":"reconnecting"}}}`, expected: 2, wantErr: true},
		{name: "too few", payload: `{"receivers":{"a":{"state":"connected"}}}`, expected: 2, wantErr: true},
		{name: "too many", payload: `{"receivers":{"a":{"state":"connected"},"b":{"state":"connected"}}}`, expected: 1, wantErr: true},
		{name: "empty", payload: `{"receivers":{}}`, expected: 1, wantErr: true},
		{name: "malformed", payload: `{"receivers":`, expected: 1, wantErr: true},
		{name: "missing receivers", payload: `{"state":"connected"}`, expected: 1, wantErr: true},
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
	if err := checkReceivers(strings.NewReader(`{"receivers":{}}`), 0); err == nil {
		t.Fatal("checkReceivers() error = nil, want positive-count validation")
	}
}
