package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseWindowBlocksUpdaterBeforeDatabaseOpen(t *testing.T) {
	root := t.TempDir()
	if err := rejectReleaseWindow(root); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "release-orchestrator")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"active.json", "maintenance.json"} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := rejectReleaseWindow(root); err == nil {
			t.Fatalf("%s did not block updater", name)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}
