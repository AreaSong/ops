package maintenance

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneArtifactsScrubsSensitiveFilesAndExpiresKnownArtifacts(t *testing.T) {
	now := time.Date(2026, 8, 9, 6, 0, 0, 0, time.UTC)
	stateRoot := t.TempDir()
	legacyRoot := t.TempDir()
	newOperation := filepath.Join(stateRoot, "operations", "123e4567-e89b-42d3-a456-426614174000")
	legacyOperation := filepath.Join(legacyRoot, "operations", "update_1786255200000_123e4567-e89b-42d3-a456-426614174001")
	oldOperation := filepath.Join(stateRoot, "operations", "123e4567-e89b-42d3-a456-426614174002")
	unknownOperation := filepath.Join(stateRoot, "operations", "manual-evidence")
	for _, directory := range []string{newOperation, legacyOperation, oldOperation, unknownOperation} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(newOperation, "http-admin-settings.json"), "secret")
	writeFile(t, filepath.Join(newOperation, "task-contract.json"), "safe")
	writeFile(t, filepath.Join(legacyOperation, "sub2api.env.before"), "password=secret")
	writeFile(t, filepath.Join(legacyOperation, "health.json"), "raw")
	writeFile(t, filepath.Join(oldOperation, "task-contract.json"), "old")
	writeFile(t, filepath.Join(unknownOperation, "sub2api.env.before"), "leave-unknown-scope")
	oldTime := now.Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(oldOperation, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	snapshots := filepath.Join(stateRoot, "snapshots")
	if err := os.MkdirAll(snapshots, 0o700); err != nil {
		t.Fatal(err)
	}
	oldSnapshot := filepath.Join(snapshots, "ops-20260701T060000Z.db")
	newSnapshot := filepath.Join(snapshots, "ops-20260809T060000Z.db")
	writeFile(t, oldSnapshot, "old")
	writeFile(t, newSnapshot, "new")
	if err := os.Chtimes(oldSnapshot, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	result, err := PruneArtifacts(stateRoot, legacyRoot, 30*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.OperationDirectories != 1 || result.Snapshots != 1 || result.SensitiveFiles != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}
	assertMissing(t, filepath.Join(newOperation, "http-admin-settings.json"))
	assertMissing(t, filepath.Join(legacyOperation, "sub2api.env.before"))
	assertMissing(t, filepath.Join(legacyOperation, "health.json"))
	assertMissing(t, oldOperation)
	assertMissing(t, oldSnapshot)
	assertExists(t, filepath.Join(newOperation, "task-contract.json"))
	assertExists(t, filepath.Join(unknownOperation, "sub2api.env.before"))
	assertExists(t, newSnapshot)
}

func TestPruneArtifactsRejectsSymlinkedOperation(t *testing.T) {
	root := t.TempDir()
	operations := filepath.Join(root, "operations")
	if err := os.MkdirAll(operations, 0o700); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	link := filepath.Join(operations, "123e4567-e89b-42d3-a456-426614174000")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := PruneArtifacts(root, "", 30*24*time.Hour, time.Now()); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestPruneArtifactsRejectsSymlinkedRoot(t *testing.T) {
	parent := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(parent, "state")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := PruneArtifacts(link, "", 30*24*time.Hour, time.Now()); err == nil {
		t.Fatal("expected root symlink rejection")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be missing, got %v", path, err)
	}
}
