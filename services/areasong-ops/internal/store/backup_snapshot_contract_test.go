package store

import (
	"database/sql"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
)

// 通过真正的 Go SQLite 驱动和迁移链验证 Python 备份合同，防止仅靠版本号夹具放行。
func TestBackupSnapshotContractMatchesRealMigrations(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("备份兼容性门禁需要 python3", err)
	}
	for _, version := range []int{45, CurrentSchemaVersion()} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			root := t.TempDir()
			database, err := sql.Open("sqlite", filepath.Join(root, "source.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if _, err := database.Exec(schema); err != nil {
				t.Fatal(err)
			}
			for _, migration := range migrations[:version] {
				if _, err := database.Exec(migration); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := database.Exec(fmt.Sprintf("PRAGMA user_version=%d", version)); err != nil {
				t.Fatal(err)
			}
			snapshot := filepath.Join(root, "snapshot.db")
			if _, err := database.Exec("VACUUM INTO ?", snapshot); err != nil {
				t.Fatal(err)
			}
			tool := "../../../../scripts/backup/areasong_ops_snapshot.py"
			output, err := exec.Command(python, "-I", "-B", tool, "validate", snapshot).CombinedOutput()
			if err != nil {
				t.Fatalf("真实 schema %d 验证失败: %v\n%s", version, err, output)
			}
		})
	}
}
