package debugapi

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/kid0317/codex-workspace-bot/internal/config"
	"github.com/kid0317/codex-workspace-bot/internal/db"
	"github.com/kid0317/codex-workspace-bot/internal/feishu"
	"github.com/kid0317/codex-workspace-bot/internal/model"
	"github.com/kid0317/codex-workspace-bot/internal/session"
	"github.com/kid0317/codex-workspace-bot/internal/task"
)

func New(cfg config.Config, managers map[string]*session.Manager) http.Handler {
	return NewWithServices(cfg, Services{Managers: managers})
}

type Services struct {
	Managers    map[string]*session.Manager
	TaskRunners map[string]*task.Runner
	Stores      map[string]*db.Store
}

func NewWithServices(cfg config.Config, services Services) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("/debug/dispatch", func(w http.ResponseWriter, r *http.Request) {
		if !requireDebug(w, r, cfg) {
			return
		}
		if !requirePost(w, r) {
			return
		}
		limitBody(w, r, cfg)
		var req struct {
			AppID       string              `json:"app_id"`
			ChatType    string              `json:"chat_type"`
			ChatID      string              `json:"chat_id"`
			ThreadID    string              `json:"thread_id"`
			SenderID    string              `json:"sender_id"`
			MessageID   string              `json:"message_id"`
			Text        string              `json:"text"`
			Scenario    string              `json:"scenario"`
			Attachments []feishu.Attachment `json:"attachments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "invalid_or_oversized_body"})
			return
		}
		mgr := services.Managers[req.AppID]
		if mgr == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown_app"})
			return
		}
		if req.ChatType == "" {
			req.ChatType = "p2p"
		}
		receiveID, receiveType := req.ChatID, "chat_id"
		if req.ChatType == "p2p" {
			receiveID, receiveType = req.SenderID, "open_id"
		}
		msg := feishu.IncomingMessage{
			AppID:       req.AppID,
			ChatType:    req.ChatType,
			ChatID:      req.ChatID,
			ThreadID:    req.ThreadID,
			ChannelKey:  feishu.BuildChannelKey(req.ChatType, req.ChatID, req.ThreadID, req.AppID),
			SenderID:    req.SenderID,
			MessageID:   req.MessageID,
			Prompt:      req.Text,
			Scenario:    req.Scenario,
			Attachments: req.Attachments,
			ReceiveID:   receiveID,
			ReceiveType: receiveType,
		}
		if err := mgr.Dispatch(r.Context(), msg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "dispatch_failed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/debug/engine/scenario", func(w http.ResponseWriter, r *http.Request) {
		if !requireDebug(w, r, cfg) {
			return
		}
		if !requirePost(w, r) {
			return
		}
		writeJSON(w, http.StatusGone, map[string]any{"error": "use_per_request_scenario"})
	})
	mux.HandleFunc("/debug/task/run", func(w http.ResponseWriter, r *http.Request) {
		if !requireDebug(w, r, cfg) {
			return
		}
		if !requirePost(w, r) {
			return
		}
		limitBody(w, r, cfg)
		var req struct {
			AppID string     `json:"app_id"`
			Task  model.Task `json:"task"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_body"})
			return
		}
		runner := services.TaskRunners[req.AppID]
		if runner == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown_app"})
			return
		}
		if req.Task.AppID != "" && req.Task.AppID != req.AppID {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "cross_app_task_rejected"})
			return
		}
		if req.Task.AppID == "" {
			req.Task.AppID = req.AppID
		}
		if req.Task.ID != "" && !strings.HasPrefix(req.Task.ID, req.AppID+"/") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "cross_app_task_rejected"})
			return
		}
		if err := runner.Run(r.Context(), req.Task); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "task_failed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("/debug/approval/respond", func(w http.ResponseWriter, r *http.Request) {
		if !requireDebug(w, r, cfg) {
			return
		}
		if !requirePost(w, r) {
			return
		}
		limitBody(w, r, cfg)
		var req struct {
			AppID      string `json:"app_id"`
			ApprovalID string `json:"approval_id"`
			Decision   string `json:"decision"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AppID == "" || req.ApprovalID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_body"})
			return
		}
		store := services.Stores[req.AppID]
		if store == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown_app"})
			return
		}
		status := ""
		switch req.Decision {
		case "allow":
			status = "user_allowed"
		case "deny":
			status = "user_denied"
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_decision"})
			return
		}
		decision, _ := json.Marshal(map[string]string{"decision": req.Decision})
		ok, err := store.Approvals().ResolvePending(req.AppID, req.ApprovalID, status, string(decision))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "approval_update_failed"})
			return
		}
		if !ok {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "approval_not_pending"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "decision": req.Decision})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func requirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return false
	}
	return true
}

func requireDebug(w http.ResponseWriter, r *http.Request, cfg config.Config) bool {
	if !cfg.Server.DebugEnabled {
		http.NotFound(w, r)
		return false
	}
	if cfg.Server.DebugToken == "" || !tokenMatches(r, cfg.Server.DebugToken) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "debug_token_required"})
		return false
	}
	return true
}

func tokenMatches(r *http.Request, token string) bool {
	if constantTimeEqual(r.Header.Get("X-Debug-Token"), token) {
		return true
	}
	auth := r.Header.Get("Authorization")
	return strings.HasPrefix(auth, "Bearer ") && constantTimeEqual(strings.TrimPrefix(auth, "Bearer "), token)
}

func constantTimeEqual(got, want string) bool {
	if got == "" || want == "" || len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func limitBody(w http.ResponseWriter, r *http.Request, cfg config.Config) {
	limit := cfg.Server.MaxBodyBytes
	if limit == 0 {
		limit = 1 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
}
