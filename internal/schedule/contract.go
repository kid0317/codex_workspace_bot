// Package schedule contains the owner-scoped scheduling contracts used by the
// Codex dynamic-tool adapter and the MySQL-backed scheduler.
package schedule

import (
	"fmt"
	"strings"
)

const ToolCatalogVersion = "s06-schedule-v4"

var dynamicToolNames = []string{"create", "list_own", "update"}

// DynamicToolNames returns a copy so callers cannot alter the catalog shared
// by future thread/start requests.
func DynamicToolNames() []string {
	return append([]string(nil), dynamicToolNames...)
}

// Owner is the trusted route-derived identity for a scheduled task. It is not
// constructed from dynamic-tool arguments.
type Owner struct {
	AppID       string
	ChatGroupID string
	OpenID      string
}

func (o Owner) Validate() error {
	if strings.TrimSpace(o.AppID) == "" {
		return fmt.Errorf("schedule owner app id is required")
	}
	if strings.TrimSpace(o.ChatGroupID) == "" {
		return fmt.Errorf("schedule owner chat group id is required")
	}
	if strings.TrimSpace(o.OpenID) == "" {
		return fmt.Errorf("schedule owner actor is required")
	}
	return nil
}
