// Package catalog holds the persistence-neutral dynamic-tool catalog upgrade
// state shared by the Codex processor and MySQL storage adapter.
package catalog

type UpgradeState string

const (
	Stable         UpgradeState = "stable"
	ArchivePending UpgradeState = "archive_pending"
	StartPending   UpgradeState = "start_pending"
)

type Upgrade struct {
	CurrentThreadID string
	CurrentVersion  string
	State           UpgradeState
	FromThreadID    string
	Target          string
}
