package main

import "testing"

func TestDefaultConfigPath(t *testing.T) {
	if defaultConfigPath() != "config.yaml" {
		t.Fatalf("defaultConfigPath = %q", defaultConfigPath())
	}
}
