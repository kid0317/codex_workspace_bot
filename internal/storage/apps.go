package storage

import (
	"context"
	"database/sql"
	"fmt"
)

type App struct {
	ID              string
	Name            string
	FeishuAppID     string
	FeishuAppSecret string
	WorkspaceDir    string
	WorkspaceMode   string
	Model           string
	ReasoningEffort string
	Enabled         bool
}

func (s *Store) ListEnabledApps(ctx context.Context) ([]App, error) {
	const query = `SELECT id, name, feishu_app_id, feishu_app_secret, workspace_dir, workspace_mode, model, reasoning_effort FROM apps WHERE enabled = TRUE ORDER BY name`
	rows, err := s.DB.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list enabled apps: %w", err)
	}
	defer rows.Close()
	var apps []App
	for rows.Next() {
		var app App
		if err := rows.Scan(&app.ID, &app.Name, &app.FeishuAppID, &app.FeishuAppSecret, &app.WorkspaceDir, &app.WorkspaceMode, &app.Model, &app.ReasoningEffort); err != nil {
			return nil, fmt.Errorf("scan enabled app: %w", err)
		}
		apps = append(apps, app)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate enabled apps: %w", err)
	}
	return apps, nil
}

func (s *Store) FindAppByName(ctx context.Context, name string) (App, error) {
	const query = `SELECT id, name, feishu_app_id, feishu_app_secret, workspace_dir, workspace_mode, model, reasoning_effort FROM apps WHERE name = ?`
	var app App
	err := s.DB.QueryRowContext(ctx, query, name).Scan(&app.ID, &app.Name, &app.FeishuAppID, &app.FeishuAppSecret, &app.WorkspaceDir, &app.WorkspaceMode, &app.Model, &app.ReasoningEffort)
	if err != nil {
		if err == sql.ErrNoRows {
			return App{}, err
		}
		return App{}, fmt.Errorf("find app: %w", err)
	}
	return app, nil
}

func (s *Store) SetAppEnabled(ctx context.Context, name string, enabled bool) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE apps SET enabled = ? WHERE name = ?`, enabled, name)
	if err != nil {
		return fmt.Errorf("set app enabled: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set app enabled rows: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("set app enabled: app %q not found", name)
	}
	return nil
}

// ListApps returns App records for local administration. Callers must never print FeishuAppSecret.
func (s *Store) ListApps(ctx context.Context) ([]App, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,name,feishu_app_id,feishu_app_secret,workspace_dir,workspace_mode,model,reasoning_effort,enabled FROM apps ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	defer rows.Close()
	var apps []App
	for rows.Next() {
		var app App
		var enabled bool
		if err := rows.Scan(&app.ID, &app.Name, &app.FeishuAppID, &app.FeishuAppSecret, &app.WorkspaceDir, &app.WorkspaceMode, &app.Model, &app.ReasoningEffort, &enabled); err != nil {
			return nil, err
		}
		app.Enabled = enabled
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

func (s *Store) DeleteApp(ctx context.Context, name string) error {
	r, err := s.DB.ExecContext(ctx, `DELETE FROM apps WHERE name=?`, name)
	if err != nil {
		return fmt.Errorf("delete app: %w", err)
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return fmt.Errorf("delete app: app %q not found", name)
	}
	return nil
}

func (s *Store) UpsertApp(ctx context.Context, app App) error {
	const query = `INSERT INTO apps
		(id, name, feishu_app_id, feishu_app_secret, workspace_dir, workspace_mode, model, reasoning_effort, enabled)
		VALUES (UUID(), ?, ?, ?, ?, ?, ?, ?, TRUE)
		ON DUPLICATE KEY UPDATE
			name = VALUES(name),
			feishu_app_id = VALUES(feishu_app_id),
			feishu_app_secret = VALUES(feishu_app_secret),
			workspace_dir = VALUES(workspace_dir),
			workspace_mode = VALUES(workspace_mode),
			model = VALUES(model),
			reasoning_effort = VALUES(reasoning_effort),
			enabled = TRUE`
	if _, err := s.DB.ExecContext(ctx, query,
		app.Name, app.FeishuAppID, app.FeishuAppSecret, app.WorkspaceDir,
		app.WorkspaceMode, app.Model, app.ReasoningEffort,
	); err != nil {
		return fmt.Errorf("upsert app %q: %w", app.Name, err)
	}
	return nil
}
