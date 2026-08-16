package codexbridge

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
)

type Config struct {
	Command string
	Args    []string
	Env     []string
}

type Bridge struct {
	config Config
	mu     sync.Mutex
	active bool
}

func New(cfg Config) *Bridge {
	return &Bridge{config: cfg}
}

func (b *Bridge) Serve(ctx context.Context, listener net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if !b.claim() {
			_, _ = io.WriteString(conn, "codex app-server already connected\n")
			_ = conn.Close()
			continue
		}
		go func() {
			defer b.release()
			_ = b.serveConnection(ctx, conn)
		}()
	}
}

func (b *Bridge) claim() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active {
		return false
	}
	b.active = true
	return true
}

func (b *Bridge) release() {
	b.mu.Lock()
	b.active = false
	b.mu.Unlock()
}

func (b *Bridge) serveConnection(ctx context.Context, conn net.Conn) error {
	defer conn.Close()
	cmd := exec.CommandContext(ctx, b.config.Command, b.config.Args...)
	cmd.Env = b.config.Env
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	copyDone := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(stdin, conn)
		_ = stdin.Close()
		copyDone <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(conn, stdout)
		copyDone <- struct{}{}
	}()

	<-copyDone
	_ = conn.Close()
	_ = stdin.Close()
	return cmd.Wait()
}
