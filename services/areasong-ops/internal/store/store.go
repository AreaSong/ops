package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
	if err := store.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("迁移 SQLite 失败: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("收紧 SQLite 权限失败: %w", err)
	}
	return store, nil
}

// OpenExisting is used by short-lived root-owned helpers. It never creates or
// chmods the shared state directory and refuses to migrate a database while
// the long-running Runner may be using it.
func OpenExisting(path string) (*Store, error) {
	if err := validateExistingStatePath(path); err != nil {
		return nil, err
	}
	// Resolve platform aliases such as macOS's /var -> /private/var before
	// checking ownership and permissions. The state file itself must still be a
	// non-symlink regular file; only the trusted parent alias is followed.
	resolvedDirectory, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, errors.New("Runner 状态目录无法解析")
	}
	directoryInfo, err := os.Lstat(resolvedDirectory)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Runner 状态目录不存在或身份无效")
	}
	fileInfo, err := os.Lstat(path)
	if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Runner SQLite 不存在或身份无效")
	}
	if err := validateStatePermissions(directoryInfo, fileInfo); err != nil {
		return nil, err
	}
	if err := validateSQLiteSidecars(path, directoryInfo); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开现有 SQLite 失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON;
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = FULL;
		PRAGMA busy_timeout = 5000;`); err != nil {
		db.Close()
		return nil, err
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != len(migrations) {
		db.Close()
		return nil, fmt.Errorf("Runner SQLite schema 版本不匹配: %d", version)
	}
	if err := validateSQLiteSidecars(path, directoryInfo); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, now: func() time.Time { return time.Now().UTC() }, path: path}, nil
}

func validateExistingStatePath(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		strings.ContainsRune(path, '\x00') {
		return errors.New("Runner SQLite 路径无效")
	}
	parent := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return errors.New("Runner SQLite 路径父目录不存在")
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Runner SQLite 路径包含不安全目录")
	}
	return nil
}

func validateStatePermissions(directoryInfo, fileInfo os.FileInfo) error {
	if directoryInfo.Mode().Perm()&0o022 != 0 {
		return errors.New("Runner 状态目录对组或其他用户可写")
	}
	if fileInfo.Mode().Perm()&0o077 != 0 {
		return errors.New("Runner SQLite 权限过宽")
	}
	if os.Geteuid() == 0 {
		if uid, gid, ok := statOwner(fileInfo); !ok || uid != 0 || gid != 0 {
			return errors.New("Runner SQLite 必须由 root:root 所有")
		}
	}
	return nil
}

func validateSQLiteSidecars(path string, directoryInfo os.FileInfo) error {
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		info, err := os.Lstat(sidecar)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Runner SQLite sidecar 身份无效: %s", filepath.Base(sidecar))
		}
		if info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("Runner SQLite sidecar 权限过宽: %s", filepath.Base(sidecar))
		}
		if os.Geteuid() == 0 {
			uid, gid, ok := statOwner(info)
			if !ok || uid != 0 || gid != 0 {
				return fmt.Errorf("Runner SQLite sidecar 必须由 root:root 所有: %s", filepath.Base(sidecar))
			}
		}
	}
	_ = directoryInfo // retained for callers that validate the directory first
	return nil
}

func statOwner(info os.FileInfo) (uid, gid uint32, ok bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return uint32(stat.Uid), uint32(stat.Gid), true
}

func (store *Store) migrate(ctx context.Context) error {
	var version int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for index := version; index < len(migrations); index++ {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[index]); err != nil {
			tx.Rollback()
			return fmt.Errorf("执行迁移 %d 失败: %w", index+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, index+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
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
