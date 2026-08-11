package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound        = errors.New("记录不存在")
	ErrPreviewConsumed = errors.New("操作预览已使用")
	ErrPreviewExpired  = errors.New("操作预览已过期")
	ErrActorMismatch   = errors.New("操作者不匹配")
	ErrConfirmation    = errors.New("确认短语不匹配")
	ErrIdempotency     = errors.New("幂等键已用于其他请求")
)

type Store struct {
	db   *sql.DB
	now  func() time.Time
	path string
}

func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建状态目录失败: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("收紧状态目录权限失败: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite 失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, now: func() time.Time { return time.Now().UTC() }, path: path}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化 SQLite 失败: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("收紧 SQLite 权限失败: %w", err)
	}
	return store, nil
}

func (store *Store) Close() error {
	return store.db.Close()
}

func encodeJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeJSON[T any](raw string, target *T) error {
	if raw == "" {
		raw = "{}"
	}
	return json.Unmarshal([]byte(raw), target)
}

func timeText(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func nullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func clampLimit(limit, maximum int) int {
	if limit <= 0 {
		return 50
	}
	if limit > maximum {
		return maximum
	}
	return limit
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
