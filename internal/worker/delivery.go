package worker

import (
	"context"
	"sync"
)

// TerminalArbiter gives one concurrent path ownership of a batch terminal
// outcome. The winning reason is immutable for the lifetime of the batch.
type TerminalArbiter struct {
	mu     sync.Mutex
	reason string
}

func NewTerminalArbiter() *TerminalArbiter { return &TerminalArbiter{} }

func (a *TerminalArbiter) Claim(reason string) bool {
	if reason == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reason != "" {
		return false
	}
	a.reason = reason
	return true
}

func (a *TerminalArbiter) Reason() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reason
}

// DeliverySlot owns the post-terminal companion delivery window. Cancellation
// can latch before publication, and callers always wait for Finish before
// releasing the channel worker.
type DeliverySlot struct {
	mu        sync.Mutex
	started   bool
	published bool
	cancelled bool
	cancel    context.CancelFunc
	done      chan struct{}
	finished  bool
}

func NewDeliverySlot() *DeliverySlot { return &DeliverySlot{done: make(chan struct{})} }

func (s *DeliverySlot) Begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.finished {
		return false
	}
	s.started = true
	return true
}

func (s *DeliverySlot) Publish(parent context.Context) (context.Context, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || s.published || s.finished || s.cancelled {
		return nil, false
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.published = true
	return ctx, true
}

func (s *DeliverySlot) CancelAndWait(ctx context.Context) error {
	s.mu.Lock()
	s.cancelled = true
	if s.cancel != nil {
		s.cancel()
	}
	done := s.done
	s.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *DeliverySlot) Finish() {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	s.finished = true
	if s.cancel != nil {
		s.cancel()
	}
	close(s.done)
	s.mu.Unlock()
}
