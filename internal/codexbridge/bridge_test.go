package codexbridge

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBridgeCopiesStdioWithoutInterpretingProtocol(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	bridge := New(Config{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestBridgeHelperProcess", "--"},
		Env:     append(os.Environ(), "CODEX_BRIDGE_HELPER=1"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bridge.Serve(ctx, listener) }()

	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	payload := `{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n"
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != payload {
		t.Fatalf("bridge response = %q, want %q", line, payload)
	}
}

func TestBridgeRejectsConcurrentAppServer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	bridge := New(Config{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestBridgeHelperProcess", "--"},
		Env:     append(os.Environ(), "CODEX_BRIDGE_HELPER=1"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bridge.Serve(ctx, listener) }()

	first, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := io.WriteString(first, "hold\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(first)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}

	second, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetReadDeadline(time.Now().Add(time.Second))
	message, err := bufio.NewReader(second).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "already connected") {
		t.Fatalf("rejection = %q", message)
	}
}

func TestBridgeHelperProcess(t *testing.T) {
	if os.Getenv("CODEX_BRIDGE_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		fmt.Fprintln(os.Stdout, scanner.Text())
	}
	os.Exit(0)
}

