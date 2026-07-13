package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var rotatedName = regexp.MustCompile(`^server-(\d{10})\.log(?:\.wf)?$`)

type switchableHandler struct {
	mu sync.RWMutex
	h  slog.Handler
}

func (h *switchableHandler) Enabled(ctx context.Context, level slog.Level) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.h.Enabled(ctx, level)
}
func (h *switchableHandler) Handle(ctx context.Context, record slog.Record) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.h.Handle(ctx, record)
}
func (h *switchableHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return &switchableHandler{h: h.h.WithAttrs(attrs)}
}
func (h *switchableHandler) WithGroup(name string) slog.Handler {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return &switchableHandler{h: h.h.WithGroup(name)}
}
func (h *switchableHandler) replace(handler slog.Handler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.h = handler
}

type Manager struct {
	dir           string
	level         slog.Level
	period        time.Time
	retentionDays int
	normal        *os.File
	wf            *os.File
	normalH       *switchableHandler
	wfH           *switchableHandler
	mu            sync.Mutex
}

func ParseLevel(value string) (slog.Level, error) {
	switch value {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q", value)
	}
}

func New(dir string, level slog.Level, now time.Time) (*Manager, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	m := &Manager{dir: dir, level: level, period: now.Truncate(time.Hour), retentionDays: 30}
	if err := m.openCurrent(); err != nil {
		return nil, err
	}
	if err := m.archiveBefore(now); err != nil {
		_ = m.Close()
		return nil, err
	}
	return m, nil
}

func (m *Manager) SetRetentionDays(days int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retentionDays = days
}

func (m *Manager) Logger() *slog.Logger         { return slog.New(m.normalH) }
func (m *Manager) WorkflowLogger() *slog.Logger { return slog.New(m.wfH) }

// WriteWorkflow preserves the workflow file's normal JSON shape while making
// the underlying handler error visible to the caller.
func (m *Manager) WriteWorkflow(ctx context.Context, message string, attrs ...slog.Attr) error {
	record := slog.NewRecord(time.Now(), slog.LevelInfo, message, 0)
	record.AddAttrs(attrs...)
	return m.wfH.Handle(ctx, record)
}

func (m *Manager) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				_ = m.Check(now)
			}
		}
	}()
}

func (m *Manager) Check(now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if now.Truncate(time.Hour).After(m.period) {
		if err := m.rotate(now); err != nil {
			return err
		}
	}
	return m.archiveBefore(now)
}

func (m *Manager) Sync() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.normal.Sync(); err != nil {
		return err
	}
	return m.wf.Sync()
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var err error
	if m.normal != nil {
		err = m.normal.Close()
		m.normal = nil
	}
	if m.wf != nil {
		if closeErr := m.wf.Close(); err == nil {
			err = closeErr
		}
		m.wf = nil
	}
	return err
}

func (m *Manager) openCurrent() error {
	normal, err := os.OpenFile(filepath.Join(m.dir, "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	wf, err := os.OpenFile(filepath.Join(m.dir, "server.log.wf"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		_ = normal.Close()
		return err
	}
	opts := &slog.HandlerOptions{Level: m.level}
	if m.normalH == nil {
		m.normalH = &switchableHandler{h: slog.NewJSONHandler(normal, opts)}
	} else {
		m.normalH.replace(slog.NewJSONHandler(normal, opts))
	}
	if m.wfH == nil {
		m.wfH = &switchableHandler{h: slog.NewJSONHandler(wf, opts)}
	} else {
		m.wfH.replace(slog.NewJSONHandler(wf, opts))
	}
	m.normal, m.wf = normal, wf
	return nil
}

func (m *Manager) rotate(now time.Time) error {
	stamp := m.period.Format("2006010215")
	if err := m.normal.Close(); err != nil {
		return err
	}
	if err := m.wf.Close(); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(m.dir, "server.log"), filepath.Join(m.dir, "server-"+stamp+".log")); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(m.dir, "server.log.wf"), filepath.Join(m.dir, "server-"+stamp+".log.wf")); err != nil {
		return err
	}
	m.normal, m.wf = nil, nil
	m.period = now.Truncate(time.Hour)
	return m.openCurrent()
}

func (m *Manager) archiveBefore(now time.Time) error {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return err
	}
	today := now.Format("20060102")
	for _, entry := range entries {
		if entry.IsDir() {
			if len(entry.Name()) == 8 && entry.Name() < now.AddDate(0, 0, -m.retentionDays).Format("20060102") {
				if err := os.RemoveAll(filepath.Join(m.dir, entry.Name())); err != nil {
					return err
				}
			}
			continue
		}
		match := rotatedName.FindStringSubmatch(entry.Name())
		if len(match) != 2 || match[1][:8] >= today {
			continue
		}
		destDir := filepath.Join(m.dir, match[1][:8])
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(m.dir, entry.Name()), filepath.Join(destDir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
