package db

import (
	"fmt"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/kid0317/codex-workspace-bot/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Store struct {
	db *gorm.DB
}

func Open(path string) (*Store, error) {
	gdb, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, fmt.Errorf("打开 sqlite: %w", err)
	}
	store := &Store{db: gdb}
	if err := store.Migrate(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Migrate() error {
	if err := s.db.AutoMigrate(&model.Channel{}, &model.Session{}, &model.Message{}, &model.Task{}, &model.Attachment{}, &model.ApprovalRequest{}, &model.Turn{}); err != nil {
		return fmt.Errorf("迁移 sqlite: %w", err)
	}
	return nil
}

func (s *Store) Exec(stmt string) error {
	return s.db.Exec(stmt).Error
}

func (s *Store) Channels() ChannelRepo       { return ChannelRepo{s.db} }
func (s *Store) Sessions() SessionRepo       { return SessionRepo{s.db} }
func (s *Store) Messages() MessageRepo       { return MessageRepo{s.db} }
func (s *Store) Attachments() AttachmentRepo { return AttachmentRepo{s.db} }
func (s *Store) Turns() TurnRepo             { return TurnRepo{s.db} }
func (s *Store) Tasks() TaskRepo             { return TaskRepo{s.db} }
func (s *Store) Approvals() ApprovalRepo     { return ApprovalRepo{s.db} }

func (s *Store) HasColumn(table, column string) bool {
	return s.db.Migrator().HasColumn(table, column)
}

type ChannelRepo struct{ db *gorm.DB }

func (r ChannelRepo) Save(ch model.Channel) error {
	return r.db.Save(&ch).Error
}

func (r ChannelRepo) Count() (int64, error) {
	var n int64
	err := r.db.Model(&model.Channel{}).Count(&n).Error
	return n, err
}

type SessionRepo struct{ db *gorm.DB }

func (r SessionRepo) Save(sess model.Session) error {
	if sess.Status == "" {
		sess.Status = model.SessionActive
	}
	return r.db.Save(&sess).Error
}

func (r SessionRepo) ByID(id string) (model.Session, error) {
	var sess model.Session
	err := r.db.First(&sess, "id = ?", id).Error
	return sess, err
}

func (r SessionRepo) ByChannel(channelKey string) ([]model.Session, error) {
	var sessions []model.Session
	err := r.db.Order("created_at asc").Find(&sessions, "channel_key = ?", channelKey).Error
	return sessions, err
}

func (r SessionRepo) ActiveByChannel(channelKey string) (model.Session, bool, error) {
	var sess model.Session
	err := r.db.First(&sess, "channel_key = ? AND status = ?", channelKey, model.SessionActive).Error
	if err == gorm.ErrRecordNotFound {
		return model.Session{}, false, nil
	}
	return sess, err == nil, err
}

func (r SessionRepo) ArchiveActive(channelKey string) error {
	return r.db.Model(&model.Session{}).Where("channel_key = ? AND status = ?", channelKey, model.SessionActive).Update("status", model.SessionArchived).Error
}

func (r SessionRepo) Count() (int64, error) {
	var n int64
	err := r.db.Model(&model.Session{}).Count(&n).Error
	return n, err
}

func (r SessionRepo) SetEngineThreadID(id, threadID string) error {
	return r.db.Model(&model.Session{}).Where("id = ?", id).Update("claude_session_id", threadID).Error
}

type MessageRepo struct{ db *gorm.DB }

func (r MessageRepo) Save(msg model.Message) error {
	return r.db.Save(&msg).Error
}

func (r MessageRepo) All() ([]model.Message, error) {
	var messages []model.Message
	err := r.db.Order("created_at asc").Find(&messages).Error
	return messages, err
}

func (r MessageRepo) ExistsFeishuMessage(messageID string) (bool, error) {
	if messageID == "" {
		return false, nil
	}
	var n int64
	err := r.db.Model(&model.Message{}).Where("feishu_msg_id = ?", messageID).Count(&n).Error
	return n > 0, err
}

type AttachmentRepo struct{ db *gorm.DB }

func (r AttachmentRepo) Save(att model.Attachment) error {
	return r.db.Save(&att).Error
}

func (r AttachmentRepo) ByChannelState(channelKey, state string) ([]model.Attachment, error) {
	var out []model.Attachment
	err := r.db.Order("created_at asc").Find(&out, "channel_key = ? AND state = ?", channelKey, state).Error
	return out, err
}

func (r AttachmentRepo) UpdateState(channelKey, fromState, toState, sessionID string) error {
	updates := map[string]any{"state": toState}
	if sessionID != "" {
		updates["session_id"] = sessionID
	}
	return r.db.Model(&model.Attachment{}).Where("channel_key = ? AND state = ?", channelKey, fromState).Updates(updates).Error
}

func (r AttachmentRepo) MarkConsumed(id, sessionID, sessionPath string) error {
	return r.db.Model(&model.Attachment{}).Where("id = ?", id).Updates(map[string]any{
		"state":        model.AttachmentConsumed,
		"session_id":   sessionID,
		"session_path": sessionPath,
	}).Error
}

func (r AttachmentRepo) ExpirePending(channelKey string) error {
	return r.db.Model(&model.Attachment{}).Where("channel_key = ? AND state = ?", channelKey, model.AttachmentPending).Update("state", model.AttachmentExpired).Error
}

type TurnRepo struct{ db *gorm.DB }

func (r TurnRepo) Save(turn model.Turn) error {
	return r.db.Save(&turn).Error
}

func (r TurnRepo) All() ([]model.Turn, error) {
	var turns []model.Turn
	err := r.db.Order("created_at asc").Find(&turns).Error
	return turns, err
}

type TaskRepo struct{ db *gorm.DB }

func (r TaskRepo) Save(task model.Task) error {
	return r.db.Save(&task).Error
}

type ApprovalRepo struct{ db *gorm.DB }

func (r ApprovalRepo) Save(req model.ApprovalRequest) error {
	return r.db.Save(&req).Error
}

func (r ApprovalRepo) ByID(id string) (model.ApprovalRequest, error) {
	var req model.ApprovalRequest
	err := r.db.First(&req, "id = ?", id).Error
	return req, err
}

func (r ApprovalRepo) Resolve(id, status, decisionJSON string) error {
	now := time.Now()
	return r.db.Model(&model.ApprovalRequest{}).Where("id = ?", id).Updates(map[string]any{
		"status":        status,
		"decision_json": decisionJSON,
		"resolved_at":   &now,
	}).Error
}

func (r ApprovalRepo) ResolvePending(appID, id, status, decisionJSON string) (bool, error) {
	now := time.Now()
	result := r.db.Model(&model.ApprovalRequest{}).
		Where("app_id = ? AND id = ? AND status = ?", appID, id, "pending_user").
		Updates(map[string]any{
			"status":        status,
			"decision_json": decisionJSON,
			"resolved_at":   &now,
		})
	return result.RowsAffected == 1, result.Error
}
