package main

import "testing"

func TestDefaultConfigPath(t *testing.T) {
	if defaultConfigPath() != "config.yaml" {
		t.Fatalf("defaultConfigPath = %q", defaultConfigPath())
	}
}

func TestNewHTTPServerHasOperationalTimeouts(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", nil)
	if srv.ReadHeaderTimeout == 0 || srv.ReadTimeout == 0 || srv.WriteTimeout == 0 || srv.IdleTimeout == 0 || srv.MaxHeaderBytes == 0 {
		t.Fatalf("server timeouts/header limit not configured: %#v", srv)
	}
}
