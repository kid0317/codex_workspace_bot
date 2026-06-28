package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Sender interface {
	SendText(ctx context.Context, receiveID, receiveType, text string) (string, error)
	SendThinking(ctx context.Context, receiveID, receiveType string) (string, error)
	UpdateCard(ctx context.Context, cardMessageID, text string) error
}

type SenderCall struct {
	Method      string
	ReceiveID   string
	ReceiveType string
	Text        string
}

type MockSender struct {
	mu    sync.Mutex
	calls []SenderCall
}

func NewMockSender() *MockSender {
	return &MockSender{}
}

func (s *MockSender) SendText(ctx context.Context, receiveID, receiveType, text string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, SenderCall{Method: "SendText", ReceiveID: receiveID, ReceiveType: receiveType, Text: text})
	return fmt.Sprintf("msg-%d", len(s.calls)), nil
}

func (s *MockSender) SendThinking(ctx context.Context, receiveID, receiveType string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, SenderCall{Method: "SendThinking", ReceiveID: receiveID, ReceiveType: receiveType})
	return fmt.Sprintf("card-%d", len(s.calls)), nil
}

func (s *MockSender) UpdateCard(ctx context.Context, cardMessageID, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, SenderCall{Method: "UpdateCard", ReceiveID: cardMessageID, Text: text})
	return nil
}

func (s *MockSender) Calls() []SenderCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SenderCall, len(s.calls))
	copy(out, s.calls)
	return out
}

func (s *MockSender) HasCallSequence(methods ...string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(methods) != len(s.calls) {
		return false
	}
	for i, method := range methods {
		if s.calls[i].Method != method {
			return false
		}
	}
	return true
}

func (s *MockSender) HasOnly(method string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return false
	}
	for _, call := range s.calls {
		if call.Method != method {
			return false
		}
	}
	return true
}

type Attachment struct {
	ID              string
	Kind            string
	OriginalName    string
	SourceMessageID string
	TempPath        string
	SessionPath     string
	SizeBytes       int64
	CreatedAt       time.Time
}

type IncomingMessage struct {
	AppID          string
	ChatType       string
	ChatID         string
	ThreadID       string
	ChannelKey     string
	SenderID       string
	MessageID      string
	Prompt         string
	Scenario       string
	SuppressOutput bool
	Attachments    []Attachment
	ReceiveID      string
	ReceiveType    string
	ReceivedAt     time.Time
}

type EventFixture struct {
	AppID       string
	ChatType    string
	ChatID      string
	ThreadID    string
	SenderID    string
	MessageID   string
	MessageType string
	Content     string
}

func Normalize(ev EventFixture) (IncomingMessage, error) {
	prompt, err := parseContent(ev.MessageType, ev.Content)
	if err != nil {
		return IncomingMessage{}, err
	}
	return IncomingMessage{
		AppID:       ev.AppID,
		ChatType:    ev.ChatType,
		ChatID:      ev.ChatID,
		ThreadID:    ev.ThreadID,
		ChannelKey:  BuildChannelKey(ev.ChatType, ev.ChatID, ev.ThreadID, ev.AppID),
		SenderID:    ev.SenderID,
		MessageID:   ev.MessageID,
		Prompt:      prompt,
		ReceiveID:   receiveID(ev.ChatType, ev.ChatID, ev.SenderID),
		ReceiveType: receiveType(ev.ChatType),
		ReceivedAt:  time.Now(),
	}, nil
}

func BuildChannelKey(chatType, chatID, threadID, appID string) string {
	if threadID != "" {
		return fmt.Sprintf("thread:%s:%s:%s", chatID, threadID, appID)
	}
	if chatType == "p2p" {
		return fmt.Sprintf("p2p:%s:%s", chatID, appID)
	}
	return fmt.Sprintf("group:%s:%s", chatID, appID)
}

func receiveType(chatType string) string {
	if chatType == "p2p" {
		return "open_id"
	}
	return "chat_id"
}

func receiveID(chatType, chatID, senderID string) string {
	if chatType == "p2p" {
		return senderID
	}
	return chatID
}

func parseContent(kind, content string) (string, error) {
	switch kind {
	case "text":
		var v struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(content), &v); err != nil {
			return "", fmt.Errorf("解析 text 消息: %w", err)
		}
		return v.Text, nil
	case "post":
		return extractPostText(content), nil
	default:
		return "", nil
	}
}

func extractPostText(content string) string {
	var v struct {
		Title   string `json:"title"`
		Content [][]struct {
			Tag  string `json:"tag"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return content
	}
	var lines []string
	if strings.TrimSpace(v.Title) != "" {
		lines = append(lines, strings.TrimSpace(v.Title))
	}
	for _, row := range v.Content {
		var b strings.Builder
		for _, part := range row {
			if part.Tag == "text" {
				b.WriteString(part.Text)
			}
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			lines = append(lines, s)
		}
	}
	return strings.Join(lines, "\n")
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func SanitizeFilename(name string) string {
	base := filepath.Base(strings.ReplaceAll(name, `\`, `/`))
	base = unsafeName.ReplaceAllString(base, "_")
	base = strings.Trim(base, "._ ")
	if base == "" {
		return "attachment"
	}
	return base
}
