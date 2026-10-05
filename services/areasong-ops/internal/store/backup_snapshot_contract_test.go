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
	for _, version := range []int{45, 47, 48, 49, CurrentSchemaVersion()} {
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
			if version == 49 {
				if _, err := database.Exec("DROP TABLE work_admission_targets"); err != nil {
					t.Fatal(err)
				}
				missing := filepath.Join(root, "missing-targets.db")
				if _, err := database.Exec("VACUUM INTO ?", missing); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command(python, "-I", "-B", tool, "validate", missing).CombinedOutput(); err == nil {
					t.Fatalf("schema49 missing targets accepted: %s", out)
				}
			}
			if version >= 48 {
				if out, err := exec.Command(python, "-I", "-B", tool, "validate", snapshot, "--allow-legacy").CombinedOutput(); err == nil {
					t.Fatalf("schema48/49 尚无恢复授权却被放行: %s", out)
				}
				if _, err := database.Exec(`ALTER TABLE tenants DROP COLUMN lifecycle_generation`); err != nil {
					t.Fatal(err)
				}
				missing := filepath.Join(root, "missing-generation.db")
				if _, err := database.Exec("VACUUM INTO ?", missing); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command(python, "-I", "-B", tool, "validate", missing).CombinedOutput(); err == nil {
					t.Fatalf("schema48/49 缺少代次列却被放行: %s", out)
				}
			}
		})
	}
}
