package model

import "time"

const (
	SessionActive   = "active"
	SessionArchived = "archived"

	MessageRoleUser      = "user"
	MessageRoleAssistant = "assistant"

	TaskModeUserFacing    = "user-facing"
	TaskModeBorrowChannel = "borrow-channel"
	TaskModeSystem        = "system"

	AttachmentPending      = "pending"
	AttachmentConsumed     = "consumed"
	AttachmentClearedByNew = "cleared_by_new"
	AttachmentExpired      = "expired"
)

type Channel struct {
	ChannelKey string `gorm:"primaryKey"`
	AppID      string `gorm:"index;not null"`
	ChatType   string `gorm:"not null"`
	ChatID     string `gorm:"not null"`
	ThreadID   string
	CreatedAt  time.Time
}

type Session struct {
	ID             string `gorm:"primaryKey"`
	ChannelKey     string `gorm:"index;not null"`
	EngineThreadID string `gorm:"column:claude_session_id"`
	Status         string `gorm:"not null;default:'active'"`
	CreatedBy      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Message struct {
	ID          string `gorm:"primaryKey"`
	SessionID   string `gorm:"index;not null"`
	SenderID    string
	Role        string `gorm:"not null"`
	Content     string `gorm:"type:text"`
	FeishuMsgID string
	CreatedAt   time.Time
}

type Task struct {
	ID          string     `gorm:"primaryKey" json:"id"`
	AppID       string     `gorm:"index;not null" json:"app_id"`
	Name        string     `json:"name"`
	CronExpr    string     `json:"cron"`
	TargetType  string     `json:"target_type"`
	TargetID    string     `json:"target_id"`
	Prompt      string     `gorm:"type:text" json:"prompt"`
	Enabled     bool       `json:"enabled"`
	SendOutput  bool       `json:"send_output"`
	PostArchive bool       `json:"post_archive"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	LastRunAt   *time.Time `json:"last_run_at"`
}

func (t Task) Mode() string {
	if t.SendOutput {
		return TaskModeUserFacing
	}
	if t.TargetType != "" && t.TargetID != "" {
		return TaskModeBorrowChannel
	}
	return TaskModeSystem
}

type Attachment struct {
	ID           string `gorm:"primaryKey"`
	AppID        string `gorm:"index"`
	ChannelKey   string `gorm:"index"`
	SessionID    string `gorm:"index"`
	State        string `gorm:"index"`
	OriginalName string
	TempPath     string
	SessionPath  string
	SourceMsgID  string `gorm:"index"`
	CreatedAt    time.Time
}

type EventReceipt struct {
	MessageID string `gorm:"primaryKey"`
	AppID     string `gorm:"index"`
	CreatedAt time.Time
}

type ApprovalRequest struct {
	ID             string `gorm:"primaryKey"`
	AppID          string `gorm:"index"`
	ChannelKey     string
	SessionID      string
	TurnID         string
	EngineThreadID string
	Status         string `gorm:"index"`
	RequestJSON    string `gorm:"type:text"`
	DecisionJSON   string `gorm:"type:text"`
	CreatedAt      time.Time
	ResolvedAt     *time.Time
	ExpiresAt      time.Time
}

type Turn struct {
	ID             string `gorm:"primaryKey"`
	AppID          string `gorm:"index"`
	ChannelKey     string `gorm:"index"`
	SessionID      string `gorm:"index"`
	TaskID         string `gorm:"index"`
	EngineThreadID string
	Status         string `gorm:"index"`
	Prompt         string `gorm:"type:text"`
	Output         string `gorm:"type:text"`
	ErrorKind      string
	InputTokens    int
	OutputTokens   int
	CreatedAt      time.Time
	CompletedAt    *time.Time
}
