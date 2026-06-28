package guardrail

import (
	"fmt"
	"time"
)

type Config struct {
	MaxMessageBytes  int
	MaxOutputBytes   int
	MaxEventsPerTurn int
	MaxTurnDuration  time.Duration
	AllowedChats     []string
}

type Guardrail struct {
	cfg Config
}

func New(cfg Config) Guardrail {
	if cfg.MaxMessageBytes == 0 {
		cfg.MaxMessageBytes = 1 << 20
	}
	if cfg.MaxOutputBytes == 0 {
		cfg.MaxOutputBytes = 256 << 10
	}
	if cfg.MaxEventsPerTurn == 0 {
		cfg.MaxEventsPerTurn = 2000
	}
	if cfg.MaxTurnDuration == 0 {
		cfg.MaxTurnDuration = 90 * time.Minute
	}
	return Guardrail{cfg: cfg}
}

func (g Guardrail) CheckInput(body, chatID string) error {
	g = New(g.cfg)
	if len([]byte(body)) > g.cfg.MaxMessageBytes {
		return fmt.Errorf("消息超过大小限制")
	}
	if len(g.cfg.AllowedChats) > 0 {
		for _, allowed := range g.cfg.AllowedChats {
			if allowed == chatID {
				return nil
			}
		}
		return fmt.Errorf("chat 不在允许列表")
	}
	return nil
}

func (g Guardrail) CheckOutput(body string) error {
	g = New(g.cfg)
	if len([]byte(body)) > g.cfg.MaxOutputBytes {
		return fmt.Errorf("输出超过大小限制")
	}
	return nil
}

func (g Guardrail) CheckEventCount(n int) error {
	g = New(g.cfg)
	if n > g.cfg.MaxEventsPerTurn {
		return fmt.Errorf("事件数量超过限制")
	}
	return nil
}
