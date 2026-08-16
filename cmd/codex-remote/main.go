package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) == 3 && os.Args[1] == "--health" {
		if err := health(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "codex bridge is not healthy")
			os.Exit(1)
		}
		return
	}
	addr := os.Getenv("CODEX_BRIDGE_ADDR")
	if addr == "" {
		fmt.Fprintln(os.Stderr, "CODEX_BRIDGE_ADDR is required")
		os.Exit(1)
	}
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to codex bridge")
		os.Exit(1)
	}
	defer conn.Close()
	done := make(chan struct{}, 1)
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		done <- struct{}{}
	}()
	_, copyErr := io.Copy(os.Stdout, conn)
	if copyErr != nil {
		fmt.Fprintln(os.Stderr, "codex bridge connection closed unexpectedly")
		os.Exit(1)
	}
	select {
	case <-done:
	default:
	}
}

func health(addr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health status %d", resp.StatusCode)
	}
	return nil
}
