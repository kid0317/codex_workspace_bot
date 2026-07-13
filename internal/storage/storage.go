package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	_ "github.com/go-sql-driver/mysql"
)

type Store struct{ DB *sql.DB }

func (s *Store) SetPool(maxOpen, maxIdle int) {
	s.DB.SetMaxOpenConns(maxOpen)
	s.DB.SetMaxIdleConns(maxIdle)
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return &Store{DB: db}, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) Migrate(ctx context.Context, dir string) error {
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version VARCHAR(255) PRIMARY KEY, applied_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3))`); err != nil {
		return fmt.Errorf("create schema migrations: %w", err)
	}
	var checksumColumn int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='schema_migrations' AND column_name='checksum'`).Scan(&checksumColumn); err != nil {
		return fmt.Errorf("check migration checksum column: %w", err)
	}
	if checksumColumn == 0 {
		if _, err := s.DB.ExecContext(ctx, `ALTER TABLE schema_migrations ADD COLUMN checksum CHAR(64) NULL`); err != nil {
			return fmt.Errorf("add migration checksum column: %w", err)
		}
	}
	var locked int
	if err := s.DB.QueryRowContext(ctx, `SELECT GET_LOCK('codex_workspace_bot_migrations', 30)`).Scan(&locked); err != nil || locked != 1 {
		if err != nil {
			return fmt.Errorf("acquire migration lock: %w", err)
		}
		return fmt.Errorf("acquire migration lock: timed out")
	}
	defer s.DB.ExecContext(context.Background(), `SELECT RELEASE_LOCK('codex_workspace_bot_migrations')`)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".sql" {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(body))
		var storedChecksum *string
		err = s.DB.QueryRowContext(ctx, `SELECT checksum FROM schema_migrations WHERE version = ?`, name).Scan(&storedChecksum)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if err == nil {
			if storedChecksum != nil && *storedChecksum != checksum {
				return fmt.Errorf("migration checksum mismatch: %s", name)
			}
			if storedChecksum == nil {
				if _, err := s.DB.ExecContext(ctx, `UPDATE schema_migrations SET checksum=? WHERE version=? AND checksum IS NULL`, checksum, name); err != nil {
					return fmt.Errorf("backfill migration checksum %s: %w", name, err)
				}
			}
			continue
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err = tx.ExecContext(ctx, string(body)); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, checksum) VALUES (?, ?)`, name, checksum)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}

func TraceID(appID, eventID string) string {
	sum := sha256.Sum256([]byte(appID + ":" + eventID))
	return fmt.Sprintf("%x", sum[:16])
}
