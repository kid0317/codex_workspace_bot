package feishu

import (
	"sync"
	"time"
)

type ReceiverStatus struct {
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Registry struct {
	mu     sync.RWMutex
	states map[string]ReceiverStatus
}

func NewRegistry() *Registry { return &Registry{states: map[string]ReceiverStatus{}} }
func (r *Registry) Set(appID, state string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states[appID] = ReceiverStatus{State: state, UpdatedAt: time.Now()}
}
func (r *Registry) Snapshot() map[string]ReceiverStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]ReceiverStatus, len(r.states))
	for k, v := range r.states {
		out[k] = v
	}
	return out
}
